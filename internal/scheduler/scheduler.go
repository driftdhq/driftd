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

	for _, stackPath := range stacks {
		job := &queue.Job{
			RepoName:   repoName,
			RepoURL:    repoURL,
			StackPath:  stackPath,
			MaxRetries: 1,
			Trigger:    "scheduled",
		}

		if err := s.queue.Enqueue(ctx, job); err != nil {
			if err == queue.ErrRepoLocked {
				log.Printf("Skipping scheduled scan for %s/%s: repo locked", repoName, stackPath)
			} else {
				log.Printf("Failed to enqueue scheduled scan for %s/%s: %v", repoName, stackPath, err)
			}
			continue
		}

		log.Printf("Enqueued scheduled scan for %s/%s", repoName, stackPath)
	}
}
