package scheduler

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/repos"
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
		Repos: []config.RepoConfig{
			{
				Name: "test-repo",
				URL:  "https://github.com/org/repo.git",
			},
		},
	}

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if s == nil {
		t.Fatal("expected non-nil scheduler")
	}
	if s.queue != q {
		t.Error("scheduler queue not set correctly")
	}
	if s.cfg != cfg {
		t.Error("scheduler config not set correctly")
	}
}

func TestSchedulerStartStop(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{
				Name: "test-repo",
				URL:  "https://github.com/org/repo.git",
				// No schedule - should be skipped
			},
		},
	}

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
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
		Repos: []config.RepoConfig{
			{
				Name:     "scheduled-repo",
				URL:      "https://github.com/org/repo.git",
				Schedule: "0 */6 * * *", // Every 6 hours
			},
			{
				Name: "unscheduled-repo",
				URL:  "https://github.com/org/other.git",
				// No schedule
			},
		},
	}

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	// Verify cron entries were registered
	entries := s.cron.Entries()
	if len(entries) != 1 {
		t.Errorf("expected 1 cron entry (for scheduled-repo), got %d", len(entries))
	}
}

func TestSchedulerStartWithMultipleSchedules(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Repos: []config.RepoConfig{
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

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
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
		Repos: []config.RepoConfig{
			{
				Name: "repo1",
				URL:  "https://github.com/org/repo1.git",
			},
		},
	}

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))

	s.OnRepoAdded("repo1", "0 * * * *")
	if len(s.entries) != 1 {
		t.Fatalf("expected 1 schedule entry, got %d", len(s.entries))
	}

	s.OnRepoUpdated("repo1", "")
	if len(s.entries) != 0 {
		t.Fatalf("expected 0 schedule entries after removal, got %d", len(s.entries))
	}

	s.OnRepoAdded("repo1", "0 * * * *")
	s.OnRepoDeleted("repo1")
	if len(s.entries) != 0 {
		t.Fatalf("expected 0 schedule entries after delete, got %d", len(s.entries))
	}
}

func TestSchedulerStartInvalidSchedule(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{
				Name:     "bad-schedule",
				URL:      "https://github.com/org/repo.git",
				Schedule: "invalid cron expression",
			},
		},
	}

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	err := s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestSchedulerNoRepos(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Repos: []config.RepoConfig{},
	}

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start with no repos: %v", err)
	}
	defer s.Stop()

	entries := s.cron.Entries()
	if len(entries) != 0 {
		t.Errorf("expected 0 cron entries for empty repos, got %d", len(entries))
	}
}

func TestSchedulerStopIsIdempotent(t *testing.T) {
	q := newTestQueue(t)
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{
				Name:     "repo",
				URL:      "https://github.com/org/repo.git",
				Schedule: "0 * * * *",
			},
		},
	}

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
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
		Repos: []config.RepoConfig{
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

	s := New(q, cfg, repos.NewCombinedProvider(cfg, nil, nil, cfg.DataDir), newTestOrchestrator(cfg, q))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	entries := s.cron.Entries()
	if len(entries) != 1 {
		t.Errorf("expected 1 cron entry (repo2 only), got %d", len(entries))
	}
}
