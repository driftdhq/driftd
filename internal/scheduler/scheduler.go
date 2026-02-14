package scheduler

import (
	"context"
	"hash/fnv"
	"log"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/repos"
	"github.com/robfig/cron/v3"
)

const scheduledScanMaxJitter = 20 * time.Second

type Scheduler struct {
	cron         *cron.Cron
	cfg          *config.Config
	provider     repos.Provider
	orchestrator *orchestrate.ScanOrchestrator

	mu      sync.Mutex
	entries map[string]cron.EntryID
}

func New(cfg *config.Config, provider repos.Provider, orch *orchestrate.ScanOrchestrator) *Scheduler {
	return &Scheduler{
		cron:         cron.New(),
		cfg:          cfg,
		provider:     provider,
		orchestrator: orch,
		entries:      make(map[string]cron.EntryID),
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
	if delay := scheduledScanJitter(repoName); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}

	ctx := context.Background()
	repoCfg, err := s.provider.Get(repoName)
	if err != nil || repoCfg == nil {
		log.Printf("Failed to find repo config for %s: %v", repoName, err)
		return
	}

	_, result, err := s.orchestrator.StartAndEnqueue(ctx, repoCfg, "scheduled", "", "")
	if err != nil {
		if err == queue.ErrRepoLocked {
			log.Printf("Skipping scheduled scan for %s: repo already running", repoName)
		} else {
			log.Printf("Failed to start scan for %s: %v", repoName, err)
		}
		return
	}

	log.Printf("Enqueued %d scheduled stacks for %s", len(result.StackIDs), repoName)
}

func scheduledScanJitter(repoName string) time.Duration {
	if repoName == "" || scheduledScanMaxJitter <= 0 {
		return 0
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(repoName))
	return time.Duration(h.Sum64() % uint64(scheduledScanMaxJitter))
}
