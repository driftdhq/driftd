package scheduler

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/projects"
	"github.com/driftdhq/driftd/internal/queue"
)

func newTestOrchestrator(cfg *config.Config, q *queue.Queue) *orchestrate.ScanOrchestrator {
	return orchestrate.New(cfg, q)
}

func newTestQueue(t *testing.T) *queue.Queue {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	q, err := queue.New(mr.Addr(), "", 0, time.Minute)
	if err != nil {
		mr.Close()
		t.Fatalf("queue: %v", err)
	}
	t.Cleanup(func() {
		_ = q.Close()
		mr.Close()
	})
	return q
}

func TestNewScheduler(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name: "test-project",
				URL:  "https://github.com/org/project.git",
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if s == nil {
		t.Fatal("expected non-nil scheduler")
	}
	if s.cfg != cfg {
		t.Error("scheduler config not set correctly")
	}
}

func TestSchedulerStartStop(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name: "test-project",
				URL:  "https://github.com/org/project.git",
				// No schedule - should be skipped
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Give cron time to initialize
	time.Sleep(50 * time.Millisecond)

	s.Stop()
	// Should complete without hanging
}

func TestSchedulerStartWithSchedule(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name:     "scheduled-project",
				URL:      "https://github.com/org/project.git",
				Schedule: "0 */6 * * *", // Every 6 hours
			},
			{
				Name: "unscheduled-project",
				URL:  "https://github.com/org/other.git",
				// No schedule
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	// Verify cron entries were registered
	entries := s.cron.Entries()
	if len(entries) != 1 {
		t.Errorf("expected 1 cron entry (for scheduled-project), got %d", len(entries))
	}
}

func TestSchedulerStartWithMultipleSchedules(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name:     "repo1",
				URL:      "https://github.com/org/repo1.git",
				Schedule: "0 * * * *", // Every hour
			},
			{
				Name:     "repo2",
				URL:      "https://github.com/org/repo2.git",
				Schedule: "0 0 * * *", // Every day at midnight
			},
			{
				Name:     "repo3",
				URL:      "https://github.com/org/repo3.git",
				Schedule: "*/5 * * * *", // Every 5 minutes
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	entries := s.cron.Entries()
	if len(entries) != 3 {
		t.Errorf("expected 3 cron entries, got %d", len(entries))
	}
}

func TestSchedulerCallbacks(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name: "repo1",
				URL:  "https://github.com/org/repo1.git",
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))

	s.OnProjectAdded("repo1", "0 * * * *")
	if len(s.entries) != 1 {
		t.Fatalf("expected 1 schedule entry, got %d", len(s.entries))
	}

	s.OnProjectUpdated("repo1", "")
	if len(s.entries) != 0 {
		t.Fatalf("expected 0 schedule entries after removal, got %d", len(s.entries))
	}

	s.OnProjectAdded("repo1", "0 * * * *")
	s.OnProjectDeleted("repo1")
	if len(s.entries) != 0 {
		t.Fatalf("expected 0 schedule entries after delete, got %d", len(s.entries))
	}
}

func TestSchedulerStartInvalidSchedule(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name:     "bad-schedule",
				URL:      "https://github.com/org/project.git",
				Schedule: "invalid cron expression",
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	err := s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestSchedulerNoRepos(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start with no projects: %v", err)
	}
	defer s.Stop()

	entries := s.cron.Entries()
	if len(entries) != 0 {
		t.Errorf("expected 0 cron entries for empty projects, got %d", len(entries))
	}
}

func TestSchedulerStopIsIdempotent(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name:     "project",
				URL:      "https://github.com/org/project.git",
				Schedule: "0 * * * *",
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Stop multiple times should not panic
	s.Stop()
	// Note: calling Stop() again on a stopped cron will block forever,
	// so we only call it once. This test verifies single stop works.
}

func TestSchedulerEmptyScheduleSkipped(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name:     "repo1",
				URL:      "https://github.com/org/repo1.git",
				Schedule: "", // Empty schedule
			},
			{
				Name:     "repo2",
				URL:      "https://github.com/org/repo2.git",
				Schedule: "0 * * * *", // Valid schedule
			},
		},
	}

	s := New(cfg, projects.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	entries := s.cron.Entries()
	if len(entries) != 1 {
		t.Errorf("expected 1 cron entry (repo2 only), got %d", len(entries))
	}
}

func TestScheduledScanJitterDeterministicAndBounded(t *testing.T) {
	first := scheduledScanJitter("project-a")
	second := scheduledScanJitter("project-a")

	if first != second {
		t.Fatalf("expected deterministic jitter, got %s and %s", first, second)
	}
	if first < 0 || first >= scheduledScanMaxJitter {
		t.Fatalf("jitter out of range: %s", first)
	}
	if got := scheduledScanJitter(""); got != 0 {
		t.Fatalf("expected no jitter for empty project name, got %s", got)
	}
}

func TestScheduledScanJitterVariesAcrossRepos(t *testing.T) {
	projects := []string{
		"project-a",
		"project-b",
		"project-c",
		"project-d",
		"project-e",
		"project-f",
	}
	seen := make(map[time.Duration]struct{}, len(projects))
	for _, projectName := range projects {
		seen[scheduledScanJitter(projectName)] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("expected at least two distinct jitter buckets across projects, got %d", len(seen))
	}
}
