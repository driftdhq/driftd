package queue

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func newTestQueue(t *testing.T) *Queue {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	q, err := New(mr.Addr(), "", 0, time.Minute)
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

func dequeueStackScan(t *testing.T, q *Queue) *StackScan {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	job, err := q.Dequeue(ctx, "worker-1")
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	return job
}

func TestStackScanRetry(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		RepoName:   "repo",
		RepoURL:    "file:///repo",
		StackPath:  "envs/dev",
		MaxRetries: 1,
	}

	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	first := dequeueStackScan(t, q)
	if err := q.Fail(ctx, first, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	retry := dequeueStackScan(t, q)
	if retry.Retries != 1 {
		t.Fatalf("expected retries=1, got %d", retry.Retries)
	}
	if retry.Status != StatusRunning {
		t.Fatalf("expected running, got %s", retry.Status)
	}

	if err := q.Complete(ctx, retry, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	final, err := q.GetStackScan(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
}

func TestStackScanRetryExhausted(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		RepoName:   "repo",
		RepoURL:    "file:///repo",
		StackPath:  "envs/dev",
		MaxRetries: 1,
	}

	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	first := dequeueStackScan(t, q)
	if err := q.Fail(ctx, first, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	second := dequeueStackScan(t, q)
	if err := q.Fail(ctx, second, "boom again"); err != nil {
		t.Fatalf("fail 2: %v", err)
	}

	final, err := q.GetStackScan(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if final.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
	if final.Retries != 2 {
		t.Fatalf("expected retries=2, got %d", final.Retries)
	}
}

func TestLockAcquisition(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	first, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if _, err := q.StartScan(ctx, "repo", "manual", "", "", 0); err != ErrRepoLocked {
		t.Fatalf("expected ErrRepoLocked, got %v", err)
	}

	locked, err := q.IsRepoLocked(ctx, "repo")
	if err != nil {
		t.Fatalf("is locked: %v", err)
	}
	if !locked {
		t.Fatalf("expected repo to be locked")
	}

	if err := q.CancelScan(ctx, first.ID, "repo", "cleanup"); err != nil {
		t.Fatalf("cancel scan: %v", err)
	}

	locked, err = q.IsRepoLocked(ctx, "repo")
	if err != nil {
		t.Fatalf("is locked after cancel: %v", err)
	}
	if locked {
		t.Fatalf("expected repo to be unlocked")
	}
}
