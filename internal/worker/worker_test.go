package worker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/storage"
)

type mockRunner struct {
	mu      sync.Mutex
	calls   []runCall
	results map[string]*storage.RunResult
	errors  map[string]error
}

type runCall struct {
	projectName   string
	stackPath     string
	tfVersion     string
	tgVersion     string
	workspacePath string
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		results: make(map[string]*storage.RunResult),
		errors:  make(map[string]error),
	}
}

func (m *mockRunner) Run(ctx context.Context, params *runner.RunParams) (*storage.RunResult, error) {
	m.mu.Lock()
	m.calls = append(m.calls, runCall{
		projectName:   params.ProjectName,
		stackPath:     params.StackPath,
		tfVersion:     params.TFVersion,
		tgVersion:     params.TGVersion,
		workspacePath: params.WorkspacePath,
	})
	m.mu.Unlock()

	key := params.ProjectName + ":" + params.StackPath
	if err, ok := m.errors[key]; ok {
		return nil, err
	}
	if result, ok := m.results[key]; ok {
		return result, nil
	}
	return &storage.RunResult{Drifted: false}, nil
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
	w := New(q, r, 2, nil, nil)

	w.Start()

	time.Sleep(50 * time.Millisecond)

	w.Stop()

}

func TestWorkerProcessesStackScan(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()
	r.results["project:envs/dev"] = &storage.RunResult{
		Drifted:   true,
		Added:     1,
		Changed:   2,
		Destroyed: 0,
	}

	w := New(q, r, 1, nil, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()
	job := &queue.StackScan{
		ProjectName: "project",
		ProjectURL:  "https://github.com/org/project.git",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetStackScan(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetStackScan(ctx, job.ID)
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
	if calls[0].projectName != "project" || calls[0].stackPath != "envs/dev" {
		t.Errorf("unexpected call: %+v", calls[0])
	}
}

func TestWorkerHandlesRunnerError(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()
	r.results["project:stack"] = &storage.RunResult{
		Error: "terraform init failed",
	}

	w := New(q, r, 1, nil, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()
	job := &queue.StackScan{
		ProjectName: "project",
		ProjectURL:  "https://github.com/org/project.git",
		StackPath:   "stack",
		MaxRetries:  0,
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetStackScan(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetStackScan(ctx, job.ID)
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

func TestWorkerUsesScanVersions(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	w := New(q, r, 1, nil, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}
	if err := q.SetScanVersions(ctx, scan.ID, "1.5.0", "0.50.0", nil, nil); err != nil {
		t.Fatalf("set versions: %v", err)
	}

	job := &queue.StackScan{
		ScanID:      scan.ID,
		ProjectName: "project",
		ProjectURL:  "https://github.com/org/project.git",
		StackPath:   "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetStackScan(ctx, job.ID)
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

	w := New(q, r, 1, nil, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	stackTF := map[string]string{"envs/dev": "1.4.0"}
	stackTG := map[string]string{"envs/dev": "0.45.0"}
	if err := q.SetScanVersions(ctx, scan.ID, "1.5.0", "0.50.0", stackTF, stackTG); err != nil {
		t.Fatalf("set versions: %v", err)
	}

	job := &queue.StackScan{
		ScanID:      scan.ID,
		ProjectName: "project",
		ProjectURL:  "https://github.com/org/project.git",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetStackScan(ctx, job.ID)
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

func TestWorkerCancelsStackScanWhenScanCanceled(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	w := New(q, r, 1, nil, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if err := q.CancelScan(ctx, scan.ID, "project", "test cancel"); err != nil {
		t.Fatalf("cancel scan: %v", err)
	}

	job := &queue.StackScan{
		ScanID:      scan.ID,
		ProjectName: "project",
		ProjectURL:  "https://github.com/org/project.git",
		StackPath:   "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetStackScan(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCanceled {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetStackScan(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != queue.StatusCanceled {
		t.Errorf("job status: got %s, want canceled", got.Status)
	}

	calls := r.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 runner calls, got %d", len(calls))
	}
}

func TestWorkerUsesWorkspacePath(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	w := New(q, r, 1, nil, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}
	if err := q.SetScanWorkspace(ctx, scan.ID, "/data/workspaces/project/123", "abc123"); err != nil {
		t.Fatalf("set workspace: %v", err)
	}

	job := &queue.StackScan{
		ScanID:      scan.ID,
		ProjectName: "project",
		ProjectURL:  "https://github.com/org/project.git",
		StackPath:   "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetStackScan(ctx, job.ID)
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
	if calls[0].workspacePath != "/data/workspaces/project/123" {
		t.Errorf("workspace path: got %q, want /data/workspaces/project/123", calls[0].workspacePath)
	}
}

func TestWorkerWithConfig(t *testing.T) {
	q := newTestQueue(t)
	r := newMockRunner()

	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name: "project",
				URL:  "https://github.com/org/project.git",
			},
		},
	}

	w := New(q, r, 1, cfg, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()
	job := &queue.StackScan{
		ProjectName: "project",
		ProjectURL:  "https://github.com/org/project.git",
		StackPath:   "stack",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := q.GetStackScan(ctx, job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status == queue.StatusCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := q.GetStackScan(ctx, job.ID)
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

	w := New(q, r, 3, nil, nil)
	w.Start()
	defer w.Stop()

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		job := &queue.StackScan{
			ProjectName: "project",
			ProjectURL:  "https://github.com/org/project.git",
			StackPath:   fmt.Sprintf("stack-%d", i),
		}
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

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
