package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/repos"
	"github.com/driftdhq/driftd/internal/stack"
	"github.com/driftdhq/driftd/internal/version"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron     *cron.Cron
	queue    *queue.Queue
	cfg      *config.Config
	provider repos.Provider

	mu      sync.Mutex
	entries map[string]cron.EntryID
}

func New(q *queue.Queue, cfg *config.Config, provider repos.Provider) *Scheduler {
	return &Scheduler{
		cron:     cron.New(),
		queue:    q,
		cfg:      cfg,
		provider: provider,
		entries:  make(map[string]cron.EntryID),
	}
}

func (s *Scheduler) Start() error {
	repos, err := s.provider.List()
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if repo.Schedule == "" {
			continue
		}
		if err := s.scheduleRepo(repo.Name, repo.Schedule); err != nil {
			return err
		}
	}

	s.cron.Start()
	return nil
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

func (s *Scheduler) OnRepoAdded(name, schedule string) {
	if schedule == "" {
		return
	}
	if err := s.scheduleRepo(name, schedule); err != nil {
		log.Printf("Failed to schedule repo %s: %v", name, err)
	}
}

func (s *Scheduler) OnRepoUpdated(name, schedule string) {
	if schedule == "" {
		s.unscheduleRepo(name)
		return
	}
	if err := s.scheduleRepo(name, schedule); err != nil {
		log.Printf("Failed to reschedule repo %s: %v", name, err)
	}
}

func (s *Scheduler) OnRepoDeleted(name string) {
	s.unscheduleRepo(name)
}

func (s *Scheduler) scheduleRepo(name, schedule string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[name]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, name)
	}

	repoName := name
	entryID, err := s.cron.AddFunc(schedule, func() {
		s.enqueueRepoScans(repoName)
	})
	if err != nil {
		return err
	}
	s.entries[name] = entryID
	log.Printf("Scheduled scans for %s: %s", name, schedule)
	return nil
}

func (s *Scheduler) unscheduleRepo(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[name]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, name)
		log.Printf("Removed schedule for %s", name)
	}
}

func (s *Scheduler) enqueueRepoScans(repoName string) {
	ctx := context.Background()
	repoCfg, err := s.provider.Get(repoName)
	if err != nil || repoCfg == nil {
		log.Printf("Failed to find repo config for %s: %v", repoName, err)
		return
	}
	repoURL := repoCfg.URL

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	scan, err := s.queue.StartScan(ctx, repoName, "scheduled", "", "", 0)
	if err != nil {
		if err == queue.ErrRepoLocked {
			activeScan, activeErr := s.queue.GetActiveScan(ctx, repoName)
			if repoCfg != nil && repoCfg.CancelInflightEnabled() && activeErr == nil && activeScan != nil {
				newPriority := queue.TriggerPriority("scheduled")
				activePriority := queue.TriggerPriority(activeScan.Trigger)
				if newPriority >= activePriority {
					_ = s.queue.CancelScan(ctx, activeScan.ID, repoName, "superseded by new schedule")
					scan, err = s.queue.StartScan(ctx, repoName, "scheduled", "", "", 0)
					if err != nil {
						log.Printf("Failed to start scan for %s after cancel: %v", repoName, err)
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
			log.Printf("Failed to start scan for %s: %v", repoName, err)
			return
		}
	}
	// Use Background context because renewal must continue independent of the scheduler tick.
	// The goroutine exits when scan status changes to completed/failed/canceled.
	go s.queue.RenewScanLock(context.Background(), scan.ID, repoName, s.cfg.Worker.ScanMaxAge, s.cfg.Worker.RenewEvery)

	auth, err := gitauth.AuthMethod(ctx, repoCfg)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoName, err.Error())
		log.Printf("Failed to resolve git auth for %s: %v", repoName, err)
		return
	}

	workspacePath, commitSHA, err := cloneWorkspace(ctx, s.cfg.DataDir, repoCfg, scan.ID, auth)
	if err != nil {
		if workspacePath != "" {
			_ = os.RemoveAll(filepath.Dir(workspacePath))
		}
		_ = s.queue.FailScan(ctx, scan.ID, repoName, err.Error())
		log.Printf("Failed to clone workspace for %s: %v", repoName, err)
		return
	}
	if err := s.queue.SetScanWorkspace(ctx, scan.ID, workspacePath, commitSHA); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoName, fmt.Sprintf("failed to set workspace: %v", err))
		log.Printf("Failed to set workspace for %s: %v", repoName, err)
		return
	}

	discovered, err := stack.Discover(workspacePath, repoCfg.IgnorePaths)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoName, err.Error())
		log.Printf("Failed to discover stacks for %s: %v", repoName, err)
		return
	}
	if len(discovered) == 0 {
		_ = s.queue.FailScan(ctx, scan.ID, repoName, "no stacks discovered")
		log.Printf("No stacks discovered for %s", repoName)
		return
	}

	versions, err := version.Detect(workspacePath, discovered)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoName, err.Error())
		log.Printf("Failed to detect versions for %s: %v", repoName, err)
		return
	}
	if err := s.queue.SetScanVersions(ctx, scan.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoName, fmt.Sprintf("failed to set versions: %v", err))
		log.Printf("Failed to set versions for %s: %v", repoName, err)
		return
	}
	if err := s.queue.SetScanTotal(ctx, scan.ID, len(discovered)); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoName, fmt.Sprintf("failed to set scan total: %v", err))
		log.Printf("Failed to set scan total for %s: %v", repoName, err)
		return
	}

	for _, stackPath := range discovered {
		job := &queue.StackScan{
			ScanID:     scan.ID,
			RepoName:   repoName,
			RepoURL:    repoURL,
			StackPath:  stackPath,
			MaxRetries: maxRetries,
			Trigger:    "scheduled",
		}

		if err := s.queue.Enqueue(ctx, job); err != nil {
			_ = s.queue.MarkScanEnqueueFailed(ctx, scan.ID)
			log.Printf("Failed to enqueue scheduled scan for %s/%s: %v", repoName, stackPath, err)
			continue
		}

		log.Printf("Enqueued scheduled scan for %s/%s", repoName, stackPath)
	}
}

func cloneWorkspace(ctx context.Context, dataDir string, repoCfg *config.RepoConfig, scanID string, auth transport.AuthMethod) (string, string, error) {
	base := filepath.Join(dataDir, "workspaces", repoCfg.Name, scanID, "repo")
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
