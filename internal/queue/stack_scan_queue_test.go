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
		RepoName:   "repo",
		RepoURL:    "file:///repo",
		StackPath:  "envs/dev",
		MaxRetries: 0,
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
		ID:        "scan-orphaned",
		RepoName:  "repo",
		RepoURL:   "file:///repo",
		StackPath: "envs/dev",
		Status:    StatusPending,
		CreatedAt: time.Now(),
	}

	if err := q.saveStackScan(ctx, job); err != nil {
		t.Fatalf("save scan: %v", err)
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
