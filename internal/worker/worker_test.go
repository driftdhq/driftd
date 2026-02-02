package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type mockRunner struct {
	mu      sync.Mutex
	calls   []runCall
	results map[string]*runner.RunResult
	errors  map[string]error
}

type runCall struct {
	repoName      string
	stackPath     string
	tfVersion     string
	tgVersion     string
	workspacePath string
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		results: make(map[string]*runner.RunResult),
		errors:  make(map[string]error),
	}
}

func (m *mockRunner) Run(ctx context.Context, repoName, repoURL, stackPath, tfVersion, tgVersion string, auth transport.AuthMethod, workspacePath string) (*runner.RunResult, error) {
	m.mu.Lock()
	m.calls = append(m.calls, runCall{
		repoName:      repoName,
		stackPath:     stackPath,
		tfVersion:     tfVersion,
		tgVersion:     tgVersion,
		workspacePath: workspacePath,
	})
	m.mu.Unlock()

	key := repoName + ":" + stackPath
	if err, ok := m.errors[key]; ok {
		return nil, err
	}
	if result, ok := m.results[key]; ok {
		return result, nil
	}
	return &runner.RunResult{Drifted: false}, nil
}

func (m *mockRunner) getCalls() []runCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]runCall{}, m.calls...)
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

func TestWorkerStartStop(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()
	w := New(q, r, 2, nil)

	w.Start()

	// Give workers time to start
	time.Sleep(50 * time.Millisecond)

	w.Stop()

	// Should complete without hanging
}

func TestWorkerProcessesJob(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()
	r.results["repo:envs/dev"] = &runner.RunResult{
		Drifted:   true,
		Added:     1,
		Changed:   2,
		Destroyed: 0,
	}

	w := New(q, r, 1, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()
	job := &queue.Job{
		RepoName:  "repo",
		RepoURL:   "https://github.com/org/repo.git",
		StackPath: "envs/dev",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for job to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != queue.StatusCompleted {
		t.Errorf("job status: got %s, want completed", got.Status)
	}

	calls := r.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 runner call, got %d", len(calls))
	}
	if calls[0].repoName != "repo" || calls[0].stackPath != "envs/dev" {
		t.Errorf("unexpected call: %+v", calls[0])
	}
}

func TestWorkerHandlesRunnerError(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()
	r.results["repo:stack"] = &runner.RunResult{
		Error: "terraform init failed",
	}

	w := New(q, r, 1, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()
	job := &queue.Job{
		RepoName:   "repo",
		RepoURL:    "https://github.com/org/repo.git",
		StackPath:  "stack",
		MaxRetries: 0,
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for job to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != queue.StatusFailed {
		t.Errorf("job status: got %s, want failed", got.Status)
	}
	if got.Error != "terraform init failed" {
		t.Errorf("job error: got %q", got.Error)
	}
}

func TestWorkerUsesTaskVersions(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	w := New(q, r, 1, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	// Create a task with version info
	task, err := q.StartTask(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if err := q.SetTaskVersions(ctx, task.ID, "1.5.0", "0.50.0", nil, nil); err != nil {
		t.Fatalf("set versions: %v", err)
	}

	job := &queue.Job{
		TaskID:    task.ID,
		RepoName:  "repo",
		RepoURL:   "https://github.com/org/repo.git",
		StackPath: "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for job to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	calls := r.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].tfVersion != "1.5.0" {
		t.Errorf("tf version: got %q, want 1.5.0", calls[0].tfVersion)
	}
	if calls[0].tgVersion != "0.50.0" {
		t.Errorf("tg version: got %q, want 0.50.0", calls[0].tgVersion)
	}
}

func TestWorkerUsesStackVersionOverride(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	w := New(q, r, 1, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	task, err := q.StartTask(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	stackTF := map[string]string{"envs/dev": "1.4.0"}
	stackTG := map[string]string{"envs/dev": "0.45.0"}
	if err := q.SetTaskVersions(ctx, task.ID, "1.5.0", "0.50.0", stackTF, stackTG); err != nil {
		t.Fatalf("set versions: %v", err)
	}

	job := &queue.Job{
		TaskID:    task.ID,
		RepoName:  "repo",
		RepoURL:   "https://github.com/org/repo.git",
		StackPath: "envs/dev",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for job to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	calls := r.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].tfVersion != "1.4.0" {
		t.Errorf("tf version: got %q, want 1.4.0 (override)", calls[0].tfVersion)
	}
	if calls[0].tgVersion != "0.45.0" {
		t.Errorf("tg version: got %q, want 0.45.0 (override)", calls[0].tgVersion)
	}
}

func TestWorkerCancelsJobWhenTaskCanceled(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	w := New(q, r, 1, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	task, err := q.StartTask(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	// Cancel the task before the job runs
	if err := q.CancelTask(ctx, task.ID, "repo", "test cancel"); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	job := &queue.Job{
		TaskID:    task.ID,
		RepoName:  "repo",
		RepoURL:   "https://github.com/org/repo.git",
		StackPath: "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for job to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCanceled {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != queue.StatusCanceled {
		t.Errorf("job status: got %s, want canceled", got.Status)
	}

	// Runner should not have been called
	calls := r.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 runner calls, got %d", len(calls))
	}
}

func TestWorkerUsesWorkspacePath(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	w := New(q, r, 1, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	task, err := q.StartTask(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if err := q.SetTaskWorkspace(ctx, task.ID, "/data/workspaces/repo/123", "abc123"); err != nil {
		t.Fatalf("set workspace: %v", err)
	}

	job := &queue.Job{
		TaskID:    task.ID,
		RepoName:  "repo",
		RepoURL:   "https://github.com/org/repo.git",
		StackPath: "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for job to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	calls := r.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].workspacePath != "/data/workspaces/repo/123" {
		t.Errorf("workspace path: got %q, want /data/workspaces/repo/123", calls[0].workspacePath)
	}
}

func TestWorkerWithConfig(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{
				Name: "repo",
				URL:  "https://github.com/org/repo.git",
			},
		},
	}

	w := New(q, r, 1, cfg)
	w.Start()
	defer w.Stop()

	ctx := context.Background()
	job := &queue.Job{
		RepoName:  "repo",
		RepoURL:   "https://github.com/org/repo.git",
		StackPath: "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait for job to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != queue.StatusCompleted {
		t.Errorf("job status: got %s, want completed", got.Status)
	}
}

func TestWorkerConcurrency(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	// Use 3 concurrent workers
	w := New(q, r, 3, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	// Enqueue 3 jobs
	for i := 0; i < 3; i++ {
		job := &queue.Job{
			RepoName:  "repo",
			RepoURL:   "https://github.com/org/repo.git",
			StackPath: "stack",
		}
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Wait for all jobs to be processed
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		calls := r.getCalls()
		if len(calls) >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	calls := r.getCalls()
	if len(calls) < 3 {
		t.Errorf("expected at least 3 runner calls, got %d", len(calls))
	}
}
