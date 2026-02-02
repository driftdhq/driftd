package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/stack"
	"github.com/driftdhq/driftd/internal/version"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron  *cron.Cron
	queue *queue.Queue
	cfg   *config.Config
}

func New(q *queue.Queue, cfg *config.Config) *Scheduler {
	return &Scheduler{
		cron:  cron.New(),
		queue: q,
		cfg:   cfg,
	}
}

func (s *Scheduler) Start() error {
	for _, repo := range s.cfg.Repos {
		if repo.Schedule == "" {
			continue
		}

		// Capture loop variables for closure
		repoName := repo.Name
		repoURL := repo.URL
		_, err := s.cron.AddFunc(repo.Schedule, func() {
			s.enqueueRepoScans(repoName, repoURL)
		})
		if err != nil {
			return err
		}

		log.Printf("Scheduled scans for %s: %s", repoName, repo.Schedule)
	}

	s.cron.Start()
	return nil
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

func (s *Scheduler) enqueueRepoScans(repoName, repoURL string) {
	ctx := context.Background()
	repoCfg := s.cfg.GetRepo(repoName)

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	task, err := s.queue.StartTask(ctx, repoName, "scheduled", "", "", 0)
	if err != nil {
		if err == queue.ErrRepoLocked {
			activeTask, activeErr := s.queue.GetActiveTask(ctx, repoName)
			if repoCfg != nil && repoCfg.CancelInflightEnabled() && activeErr == nil && activeTask != nil {
				newPriority := queue.TriggerPriority("scheduled")
				activePriority := queue.TriggerPriority(activeTask.Trigger)
				if newPriority >= activePriority {
					_ = s.queue.CancelTask(ctx, activeTask.ID, repoName, "superseded by new schedule")
					task, err = s.queue.StartTask(ctx, repoName, "scheduled", "", "", 0)
					if err != nil {
						log.Printf("Failed to start task for %s after cancel: %v", repoName, err)
						return
					}
				} else {
					log.Printf("Skipping scheduled scan for %s: repo already running", repoName)
					return
				}
			} else {
				log.Printf("Skipping scheduled scan for %s: repo already running", repoName)
				return
			}
		} else {
			log.Printf("Failed to start task for %s: %v", repoName, err)
			return
		}
	}
	// Use Background context because renewal must continue independent of the scheduler tick.
	// The goroutine exits when task status changes to completed/failed/canceled.
	go s.queue.RenewTaskLock(context.Background(), task.ID, repoName, s.cfg.Worker.TaskMaxAge, s.cfg.Worker.RenewEvery)

	auth, err := gitauth.AuthMethod(ctx, repoCfg)
	if err != nil {
		_ = s.queue.FailTask(ctx, task.ID, repoName, err.Error())
		log.Printf("Failed to resolve git auth for %s: %v", repoName, err)
		return
	}

	workspacePath, commitSHA, err := cloneWorkspace(ctx, s.cfg.DataDir, repoCfg, task.ID, auth)
	if err != nil {
		if workspacePath != "" {
			_ = os.RemoveAll(filepath.Dir(workspacePath))
		}
		_ = s.queue.FailTask(ctx, task.ID, repoName, err.Error())
		log.Printf("Failed to clone workspace for %s: %v", repoName, err)
		return
	}
	if err := s.queue.SetTaskWorkspace(ctx, task.ID, workspacePath, commitSHA); err != nil {
		_ = s.queue.FailTask(ctx, task.ID, repoName, fmt.Sprintf("failed to set workspace: %v", err))
		log.Printf("Failed to set workspace for %s: %v", repoName, err)
		return
	}

	discovered, err := stack.Discover(workspacePath, repoCfg.IgnorePaths)
	if err != nil {
		_ = s.queue.FailTask(ctx, task.ID, repoName, err.Error())
		log.Printf("Failed to discover stacks for %s: %v", repoName, err)
		return
	}
	if len(discovered) == 0 {
		_ = s.queue.FailTask(ctx, task.ID, repoName, "no stacks discovered")
		log.Printf("No stacks discovered for %s", repoName)
		return
	}

	versions, err := version.Detect(workspacePath, discovered)
	if err != nil {
		_ = s.queue.FailTask(ctx, task.ID, repoName, err.Error())
		log.Printf("Failed to detect versions for %s: %v", repoName, err)
		return
	}
	if err := s.queue.SetTaskVersions(ctx, task.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt); err != nil {
		_ = s.queue.FailTask(ctx, task.ID, repoName, fmt.Sprintf("failed to set versions: %v", err))
		log.Printf("Failed to set versions for %s: %v", repoName, err)
		return
	}
	if err := s.queue.SetTaskTotal(ctx, task.ID, len(discovered)); err != nil {
		_ = s.queue.FailTask(ctx, task.ID, repoName, fmt.Sprintf("failed to set task total: %v", err))
		log.Printf("Failed to set task total for %s: %v", repoName, err)
		return
	}

	for _, stackPath := range discovered {
		job := &queue.Job{
			TaskID:     task.ID,
			RepoName:   repoName,
			RepoURL:    repoURL,
			StackPath:  stackPath,
			MaxRetries: maxRetries,
			Trigger:    "scheduled",
		}

		if err := s.queue.Enqueue(ctx, job); err != nil {
			_ = s.queue.MarkTaskEnqueueFailed(ctx, task.ID)
			log.Printf("Failed to enqueue scheduled scan for %s/%s: %v", repoName, stackPath, err)
			continue
		}

		log.Printf("Enqueued scheduled scan for %s/%s", repoName, stackPath)
	}
}

func cloneWorkspace(ctx context.Context, dataDir string, repoCfg *config.RepoConfig, taskID string, auth transport.AuthMethod) (string, string, error) {
	base := filepath.Join(dataDir, "workspaces", repoCfg.Name, taskID, "repo")
	if err := os.MkdirAll(filepath.Dir(base), 0755); err != nil {
		return base, "", err
	}

	cloneOpts := &git.CloneOptions{
		URL:   repoCfg.URL,
		Depth: 1,
		Auth:  auth,
	}
	if repoCfg.Branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(repoCfg.Branch)
		cloneOpts.SingleBranch = true
	}
	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	repo, err := git.PlainCloneContext(cloneCtx, base, false, cloneOpts)
	if err != nil {
		return base, "", err
	}

	head, err := repo.Head()
	if err != nil {
		return base, "", nil
	}
	return base, head.Hash().String(), nil
}
