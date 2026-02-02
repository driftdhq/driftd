package queue

import (
	"context"
	"testing"
)

func getScan(t *testing.T, q *Queue, scanID string) *Scan {
	t.Helper()

	scan, err := q.GetScan(context.Background(), scanID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	return scan
}

func TestScanLifecycle(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()

		scan, err := q.StartScan(ctx, "repo", "manual", "", "", 1)
		if err != nil {
			t.Fatalf("start scan: %v", err)
		}

		job := &StackScan{
			ScanID:     scan.ID,
			RepoName:   "repo",
			RepoURL:    "file:///repo",
			StackPath:  "envs/dev",
			MaxRetries: 0,
		}
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}

		deq := dequeueStackScan(t, q)
		if err := q.Complete(ctx, deq, false); err != nil {
			t.Fatalf("complete: %v", err)
		}

		final := getScan(t, q, scan.ID)
		if final.Status != ScanStatusCompleted {
			t.Fatalf("expected completed, got %s", final.Status)
		}
	})

	t.Run("failed", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()

		scan, err := q.StartScan(ctx, "repo", "manual", "", "", 1)
		if err != nil {
			t.Fatalf("start scan: %v", err)
		}

		job := &StackScan{
			ScanID:     scan.ID,
			RepoName:   "repo",
			RepoURL:    "file:///repo",
			StackPath:  "envs/dev",
			MaxRetries: 0,
		}
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}

		deq := dequeueStackScan(t, q)
		if err := q.Fail(ctx, deq, "boom"); err != nil {
			t.Fatalf("fail: %v", err)
		}

		final := getScan(t, q, scan.ID)
		if final.Status != ScanStatusFailed {
			t.Fatalf("expected failed, got %s", final.Status)
		}
	})
}

func TestMaybeFinishScan(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	jobs := []*StackScan{
		{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0},
		{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/prod", MaxRetries: 0},
	}
	for _, job := range jobs {
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	first := dequeueStackScan(t, q)
	if err := q.Complete(ctx, first, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	intermediate := getScan(t, q, scan.ID)
	if intermediate.Status != ScanStatusRunning {
		t.Fatalf("expected running after first completion, got %s", intermediate.Status)
	}

	second := dequeueStackScan(t, q)
	if err := q.Fail(ctx, second, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	final := getScan(t, q, scan.ID)
	if final.Status != ScanStatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
}

func TestScanCounterAccuracy(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if got := getScan(t, q, scan.ID); got.Queued != 2 || got.Running != 0 {
		t.Fatalf("unexpected initial counts: queued=%d running=%d", got.Queued, got.Running)
	}

	jobs := []*StackScan{
		{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0},
		{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/prod", MaxRetries: 0},
	}
	for _, job := range jobs {
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	first := dequeueStackScan(t, q)
	state := getScan(t, q, scan.ID)
	if state.Running != 1 || state.Queued != 1 {
		t.Fatalf("unexpected counts after dequeue: queued=%d running=%d", state.Queued, state.Running)
	}

	if err := q.Complete(ctx, first, true); err != nil {
		t.Fatalf("complete: %v", err)
	}
	state = getScan(t, q, scan.ID)
	if state.Completed != 1 || state.Drifted != 1 || state.Running != 0 || state.Queued != 1 {
		t.Fatalf("unexpected counts after complete: queued=%d running=%d completed=%d drifted=%d", state.Queued, state.Running, state.Completed, state.Drifted)
	}

	second := dequeueStackScan(t, q)
	state = getScan(t, q, scan.ID)
	if state.Running != 1 || state.Queued != 0 {
		t.Fatalf("unexpected counts after second dequeue: queued=%d running=%d", state.Queued, state.Running)
	}

	if err := q.Fail(ctx, second, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	state = getScan(t, q, scan.ID)
	if state.Failed != 1 || state.Errored != 1 || state.Completed != 1 || state.Drifted != 1 {
		t.Fatalf("unexpected final counts: completed=%d failed=%d errored=%d drifted=%d", state.Completed, state.Failed, state.Errored, state.Drifted)
	}
}

func TestCancelScan(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if err := q.CancelScan(ctx, scan.ID, "repo", "user canceled"); err != nil {
		t.Fatalf("cancel scan: %v", err)
	}

	canceled := getScan(t, q, scan.ID)
	if canceled.Status != ScanStatusCanceled {
		t.Fatalf("expected canceled, got %s", canceled.Status)
	}
	if canceled.Error != "user canceled" {
		t.Fatalf("expected cancel reason, got %q", canceled.Error)
	}

	if _, err := q.GetActiveScan(ctx, "repo"); err != ErrScanNotFound {
		t.Fatalf("expected no active scan, got %v", err)
	}

	locked, err := q.IsRepoLocked(ctx, "repo")
	if err != nil {
		t.Fatalf("is locked: %v", err)
	}
	if locked {
		t.Fatalf("expected repo unlocked")
	}
}

func TestGetLastScan(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	job := &StackScan{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deq := dequeueStackScan(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	last, err := q.GetLastScan(ctx, "repo")
	if err != nil {
		t.Fatalf("get last: %v", err)
	}
	if last.ID != scan.ID {
		t.Fatalf("expected last scan %s, got %s", scan.ID, last.ID)
	}
	if last.Status != ScanStatusCompleted {
		t.Fatalf("expected completed, got %s", last.Status)
	}
}
