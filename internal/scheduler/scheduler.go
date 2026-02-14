package scheduler

import (
	"context"
	"hash/fnv"
	"log"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/projects"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/robfig/cron/v3"
)

const scheduledScanMaxJitter = 20 * time.Second

type Scheduler struct {
	cron         *cron.Cron
	cfg          *config.Config
	provider     projects.Provider
	orchestrator *orchestrate.ScanOrchestrator

	mu      sync.Mutex
	entries map[string]cron.EntryID
}

func New(cfg *config.Config, provider projects.Provider, orch *orchestrate.ScanOrchestrator) *Scheduler {
	return &Scheduler{
		cron:         cron.New(),
		cfg:          cfg,
		provider:     provider,
		orchestrator: orch,
		entries:      make(map[string]cron.EntryID),
	}
}

func (s *Scheduler) Start() error {
	projects, err := s.provider.List()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if project.Schedule == "" {
			continue
		}
		if err := s.scheduleRepo(project.Name, project.Schedule); err != nil {
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

func (s *Scheduler) OnProjectAdded(name, schedule string) {
	if schedule == "" {
		return
	}
	if err := s.scheduleRepo(name, schedule); err != nil {
		log.Printf("Failed to schedule project %s: %v", name, err)
	}
}

func (s *Scheduler) OnProjectUpdated(name, schedule string) {
	if schedule == "" {
		s.unscheduleRepo(name)
		return
	}
	if err := s.scheduleRepo(name, schedule); err != nil {
		log.Printf("Failed to reschedule project %s: %v", name, err)
	}
}

func (s *Scheduler) OnProjectDeleted(name string) {
	s.unscheduleRepo(name)
}

func (s *Scheduler) scheduleRepo(name, schedule string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[name]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, name)
	}

	projectName := name
	entryID, err := s.cron.AddFunc(schedule, func() {
		s.enqueueProjectScans(projectName)
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

func (s *Scheduler) enqueueProjectScans(projectName string) {
	if delay := scheduledScanJitter(projectName); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}

	ctx := context.Background()
	projectCfg, err := s.provider.Get(projectName)
	if err != nil || projectCfg == nil {
		log.Printf("Failed to find project config for %s: %v", projectName, err)
		return
	}

	_, result, err := s.orchestrator.StartAndEnqueue(ctx, projectCfg, "scheduled", "", "")
	if err != nil {
		if err == queue.ErrProjectLocked {
			log.Printf("Skipping scheduled scan for %s: project already running", projectName)
		} else {
			log.Printf("Failed to start scan for %s: %v", projectName, err)
		}
		return
	}

	log.Printf("Enqueued %d scheduled stacks for %s", len(result.StackIDs), projectName)
}

func scheduledScanJitter(projectName string) time.Duration {
	if projectName == "" || scheduledScanMaxJitter <= 0 {
		return 0
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(projectName))
	return time.Duration(h.Sum64() % uint64(scheduledScanMaxJitter))
}
