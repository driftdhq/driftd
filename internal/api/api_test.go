package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cbrown132/driftd/internal/config"
	"github.com/cbrown132/driftd/internal/queue"
	"github.com/cbrown132/driftd/internal/runner"
	"github.com/cbrown132/driftd/internal/storage"
	"github.com/cbrown132/driftd/internal/worker"
)

type fakeRunner struct {
	mu       sync.Mutex
	drifted  map[string]bool
	failures map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, repoName, repoURL, stackPath string) (*runner.RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.failures[stackPath]; ok {
		return &runner.RunResult{RunAt: time.Now(), Error: err.Error()}, nil
	}

	drifted := f.drifted[stackPath]
	return &runner.RunResult{
		Drifted: drifted,
		RunAt:   time.Now(),
	}, nil
}

type scanResp struct {
	Jobs       []string    `json:"jobs"`
	Task       *queue.Task `json:"task"`
	ActiveTask *queue.Task `json:"active_task"`
	Error      string      `json:"error"`
}

func TestScanRepoCompletesTask(t *testing.T) {
	runner := &fakeRunner{
		drifted: map[string]bool{
			"envs/prod": true,
			"envs/dev":  false,
		},
	}

	ts, q, cleanup := newTestServer(t, runner, []string{"envs/prod", "envs/dev"}, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sr.Task == nil || sr.Task.ID == "" {
		t.Fatalf("expected task in response")
	}
	if len(sr.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(sr.Jobs))
	}

	task := waitForTask(t, ts, sr.Task.ID, 5*time.Second)
	if task.Status != queue.TaskStatusCompleted {
		t.Fatalf("expected completed, got %s", task.Status)
	}
	if task.Total != 2 || task.Completed != 2 {
		t.Fatalf("unexpected counts: total=%d completed=%d", task.Total, task.Completed)
	}
	if task.Drifted != 1 || task.Failed != 0 {
		t.Fatalf("unexpected drift/failed: drifted=%d failed=%d", task.Drifted, task.Failed)
	}

	// Ensure a new task can start after completion.
	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on second scan, got %d", resp2.StatusCode)
	}

	_ = q.Close()
}

func TestScanRepoConflict(t *testing.T) {
	runner := &fakeRunner{}

	ts, q, cleanup := newTestServer(t, runner, []string{"envs/prod"}, false)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sr.Task == nil || sr.Task.ID == "" {
		t.Fatalf("expected task in response")
	}

	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp2.StatusCode)
	}

	var sr2 scanResp
	if err := json.NewDecoder(resp2.Body).Decode(&sr2); err != nil {
		t.Fatalf("decode response 2: %v", err)
	}
	if sr2.ActiveTask == nil || sr2.ActiveTask.ID != sr.Task.ID {
		t.Fatalf("expected active_task to match original task")
	}

	_ = q.FailTask(context.Background(), sr.Task.ID, "repo", "test cleanup")
}

func newTestServer(t *testing.T, r worker.Runner, stacks []string, startWorker bool) (*httptest.Server, *queue.Queue, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	cfg := &config.Config{
		DataDir: t.TempDir(),
		Redis: config.RedisConfig{
			Addr: mr.Addr(),
			DB:   0,
		},
		Worker: config.WorkerConfig{
			Concurrency: 1,
			LockTTL:     2 * time.Minute,
			RetryOnce:   false,
			TaskMaxAge:  1 * time.Minute,
			RenewEvery:  10 * time.Second,
		},
		Repos: []config.RepoConfig{
			{
				Name:   "repo",
				URL:    "https://example.com/repo.git",
				Stacks: stacks,
			},
		},
	}

	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Worker.LockTTL)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}

	store := storage.New(cfg.DataDir)
	templatesFS := os.DirFS("testdata")
	staticFS := os.DirFS("testdata")

	srv, err := New(cfg, store, q, templatesFS, staticFS)
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	server := httptest.NewServer(srv.Handler())

	var w *worker.Worker
	if startWorker {
		w = worker.New(q, r, 1)
		w.Start()
	}

	cleanup := func() {
		if w != nil {
			w.Stop()
		}
		server.Close()
		_ = q.Close()
		mr.Close()
	}

	return server, q, cleanup
}

func waitForTask(t *testing.T, ts *httptest.Server, taskID string, timeout time.Duration) *queue.Task {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/tasks/" + taskID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		var task queue.Task
		if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
			resp.Body.Close()
			t.Fatalf("decode task: %v", err)
		}
		resp.Body.Close()

		if task.Status != queue.TaskStatusRunning {
			return &task
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("task %s did not complete within timeout", taskID)
	return nil
}
