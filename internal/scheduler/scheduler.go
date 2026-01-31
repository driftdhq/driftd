package scheduler

import (
	"context"
	"log"

	"github.com/cbrown132/driftd/internal/config"
	"github.com/cbrown132/driftd/internal/queue"
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
		stacks := repo.Stacks

		_, err := s.cron.AddFunc(repo.Schedule, func() {
			s.enqueueRepoScans(repoName, repoURL, stacks)
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

func (s *Scheduler) enqueueRepoScans(repoName, repoURL string, stacks []string) {
	ctx := context.Background()

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	task, err := s.queue.StartTask(ctx, repoName, "scheduled", "", "", len(stacks))
	if err != nil {
		if err == queue.ErrRepoLocked {
			log.Printf("Skipping scheduled scan for %s: repo already running", repoName)
			return
		}
		log.Printf("Failed to start task for %s: %v", repoName, err)
		return
	}
	go s.queue.RenewTaskLock(context.Background(), task.ID, repoName, s.cfg.Worker.TaskMaxAge, s.cfg.Worker.RenewEvery)

	for _, stackPath := range stacks {
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
