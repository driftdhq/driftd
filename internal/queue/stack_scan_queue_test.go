package queue

import (
	"context"
	"testing"
	"time"
)

func TestClaimAndMarkRunningPreventsDoubleClaim(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
		MaxRetries:  0,
	}

	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	scan, err := q.GetStackScan(ctx, job.ID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}

	if err := q.claimAndMarkRunning(ctx, scan, "worker-1"); err != nil {
		t.Fatalf("claim running: %v", err)
	}
	if err := q.claimAndMarkRunning(ctx, scan, "worker-2"); err != ErrAlreadyClaimed {
		t.Fatalf("expected ErrAlreadyClaimed, got %v", err)
	}
}

func TestRecoverOrphanedStackScans(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		ID:          "scan-orphaned",
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}

	if err := q.saveStackScan(ctx, job); err != nil {
		t.Fatalf("save scan: %v", err)
	}
	if err := q.client.SAdd(ctx, keyStackScanPending, job.ID).Err(); err != nil {
		t.Fatalf("add pending: %v", err)
	}

	// No queue entry yet, so it is orphaned.
	recovered, err := q.RecoverOrphanedStackScans(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered, got %d", recovered)
	}

	dequeued := dequeueStackScan(t, q)
	if dequeued.ID != job.ID {
		t.Fatalf("expected dequeued %s, got %s", job.ID, dequeued.ID)
	}
}

func TestPendingSetClearedWhenClaimed(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deq := dequeueStackScan(t, q)
	if deq == nil {
		t.Fatal("expected dequeued stack")
	}

	if count, err := q.client.SCard(ctx, keyStackScanPending).Result(); err != nil || count != 0 {
		t.Fatalf("expected pending set empty, got %d (err=%v)", count, err)
	}
}

func TestEnqueueDeduplication(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	dup := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, dup); err != ErrStackScanInflight {
		t.Fatalf("expected ErrStackScanInflight, got %v", err)
	}
}

func TestInflightClearedOnComplete(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deq := dequeueStackScan(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	again := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, again); err != nil {
		t.Fatalf("expected enqueue after complete, got %v", err)
	}
}

func TestInflightRetainedUntilFinalFailure(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	job := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
		MaxRetries:  1,
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	first := dequeueStackScan(t, q)
	if err := q.Fail(ctx, first, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	dup := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, dup); err != ErrStackScanInflight {
		t.Fatalf("expected ErrStackScanInflight during retry, got %v", err)
	}

	retry := dequeueStackScan(t, q)
	if err := q.Fail(ctx, retry, "boom again"); err != nil {
		t.Fatalf("fail retry: %v", err)
	}

	after := &StackScan{
		ProjectName: "project",
		ProjectURL:  "file:///project",
		StackPath:   "envs/dev",
	}
	if err := q.Enqueue(ctx, after); err != nil {
		t.Fatalf("expected enqueue after final failure, got %v", err)
	}
}
