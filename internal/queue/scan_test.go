package queue

import (
	"context"
	"testing"
	"time"
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

func TestZeroStackScanAutoFinishesAsFailed(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	// Trigger a transition to run the script; no stacks exist so enqueue fails.
	if err := q.MarkScanEnqueueFailed(ctx, scan.ID); err != nil {
		t.Fatalf("mark enqueue failed: %v", err)
	}

	final := getScan(t, q, scan.ID)
	if final.Status != ScanStatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
	if final.EndedAt.IsZero() {
		t.Fatal("expected ended_at to be set")
	}
	locked, _ := q.IsRepoLocked(ctx, "repo")
	if locked {
		t.Fatal("expected repo unlocked after zero-stack scan")
	}
}

func TestFailScanDoesNotDeleteOtherScansLock(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	// Start scan A and let it acquire the lock.
	scanA, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan A: %v", err)
	}

	// Simulate lock expiry: delete it manually, then start scan B which acquires a new lock.
	if err := q.client.Del(ctx, keyLockPrefix+"repo").Err(); err != nil {
		t.Fatalf("simulate lock expiry: %v", err)
	}
	// Also clean up the active-scan pointer so StartScan can proceed.
	if err := q.client.Del(ctx, keyScanRepo+"repo").Err(); err != nil {
		t.Fatalf("cleanup active scan pointer: %v", err)
	}

	scanB, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan B: %v", err)
	}

	// Now fail scan A — it should NOT delete scan B's lock.
	if err := q.FailScan(ctx, scanA.ID, "repo", "timed out"); err != nil {
		t.Fatalf("fail scan A: %v", err)
	}

	// Verify scan B's lock is still held.
	lockVal, err := q.client.Get(ctx, keyLockPrefix+"repo").Result()
	if err != nil {
		t.Fatalf("get lock after fail: %v", err)
	}
	if lockVal != scanB.ID {
		t.Fatalf("expected lock owned by scan B (%s), got %s", scanB.ID, lockVal)
	}
}

func TestCancelScanDoesNotDeleteOtherScansLock(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scanA, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan A: %v", err)
	}

	// Simulate lock expiry + re-acquisition by scan B.
	q.client.Del(ctx, keyLockPrefix+"repo")
	q.client.Del(ctx, keyScanRepo+"repo")

	scanB, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan B: %v", err)
	}

	if err := q.CancelScan(ctx, scanA.ID, "repo", "stale"); err != nil {
		t.Fatalf("cancel scan A: %v", err)
	}

	lockVal, err := q.client.Get(ctx, keyLockPrefix+"repo").Result()
	if err != nil {
		t.Fatalf("get lock after cancel: %v", err)
	}
	if lockVal != scanB.ID {
		t.Fatalf("expected lock owned by scan B (%s), got %s", scanB.ID, lockVal)
	}
}

func TestReleaseOwnedLockOnlyReleasesOwnLock(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	// Set a lock owned by "scan-1".
	q.client.Set(ctx, keyLockPrefix+"repo", "scan-1", time.Minute)

	// Attempt to release with wrong owner — should be a no-op.
	if err := q.releaseOwnedLock(ctx, "repo", "scan-2"); err != nil {
		t.Fatalf("releaseOwnedLock: %v", err)
	}
	locked, _ := q.IsRepoLocked(ctx, "repo")
	if !locked {
		t.Fatal("expected lock to still be held after wrong-owner release")
	}

	// Release with correct owner.
	if err := q.releaseOwnedLock(ctx, "repo", "scan-1"); err != nil {
		t.Fatalf("releaseOwnedLock: %v", err)
	}
	locked, _ = q.IsRepoLocked(ctx, "repo")
	if locked {
		t.Fatal("expected lock to be released after correct-owner release")
	}
}

func TestReleaseOwnedLockNoopWhenNoLock(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	// No lock exists — should not error.
	if err := q.releaseOwnedLock(ctx, "repo", "scan-1"); err != nil {
		t.Fatalf("releaseOwnedLock on missing lock: %v", err)
	}
}

func TestMarkScanEnqueueFailed(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	// One enqueue fails immediately.
	if err := q.MarkScanEnqueueFailed(ctx, scan.ID); err != nil {
		t.Fatalf("mark enqueue failed: %v", err)
	}

	state := getScan(t, q, scan.ID)
	if state.Queued != 1 {
		t.Fatalf("expected queued=1, got %d", state.Queued)
	}
	if state.Failed != 1 || state.Errored != 1 {
		t.Fatalf("expected failed=1 errored=1, got failed=%d errored=%d", state.Failed, state.Errored)
	}
	// Scan should still be running — only 1 of 2 resolved.
	if state.Status != ScanStatusRunning {
		t.Fatalf("expected running, got %s", state.Status)
	}

	// Now enqueue the second one and complete it.
	job := &StackScan{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deq := dequeueStackScan(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Scan should auto-finish as failed (since there's 1 failure).
	final := getScan(t, q, scan.ID)
	if final.Status != ScanStatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
}

func TestMarkScanEnqueueFailedAutoFinishes(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	// Single-stack scan where the only enqueue fails.
	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if err := q.MarkScanEnqueueFailed(ctx, scan.ID); err != nil {
		t.Fatalf("mark enqueue failed: %v", err)
	}

	final := getScan(t, q, scan.ID)
	if final.Status != ScanStatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
	if final.EndedAt.IsZero() || final.EndedAt.Unix() == 0 {
		t.Fatal("expected ended_at to be set")
	}

	// Lock should be released and last scan set.
	locked, _ := q.IsRepoLocked(ctx, "repo")
	if locked {
		t.Fatal("expected repo unlocked after auto-finish")
	}
	last, err := q.GetLastScan(ctx, "repo")
	if err != nil {
		t.Fatalf("get last scan: %v", err)
	}
	if last.ID != scan.ID {
		t.Fatalf("expected last scan %s, got %s", scan.ID, last.ID)
	}
}

func TestCounterFloorAtZero(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	// Directly call markScanStackScanCompleted without a prior running transition.
	// The running counter starts at 0 and the decrement should floor at 0, not go to -1.
	if err := q.markScanStackScanCompleted(ctx, scan.ID, false); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	state := getScan(t, q, scan.ID)
	if state.Running != 0 {
		t.Fatalf("expected running=0 (floored), got %d", state.Running)
	}
	if state.Completed != 1 {
		t.Fatalf("expected completed=1, got %d", state.Completed)
	}
}

func TestAutoFinishSetsEndedAt(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()
	before := time.Now().Unix()

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

	final := getScan(t, q, scan.ID)
	after := time.Now().Unix()

	if final.EndedAt.Unix() < before || final.EndedAt.Unix() > after {
		t.Fatalf("ended_at %d not between %d and %d", final.EndedAt.Unix(), before, after)
	}
}

func TestAutoFinishReleasesLockAndSetsLast(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	// Verify lock is held.
	locked, _ := q.IsRepoLocked(ctx, "repo")
	if !locked {
		t.Fatal("expected repo locked after start")
	}

	job := &StackScan{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deq := dequeueStackScan(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Lock should be released.
	locked, _ = q.IsRepoLocked(ctx, "repo")
	if locked {
		t.Fatal("expected repo unlocked after auto-finish")
	}

	// Active scan pointer should be cleared.
	if _, err := q.GetActiveScan(ctx, "repo"); err != ErrScanNotFound {
		t.Fatalf("expected no active scan, got %v", err)
	}

	// Last scan should be set.
	last, err := q.GetLastScan(ctx, "repo")
	if err != nil {
		t.Fatalf("get last: %v", err)
	}
	if last.ID != scan.ID {
		t.Fatalf("expected last=%s, got %s", scan.ID, last.ID)
	}
}

func TestAutoFinishDoesNotDeleteOtherScansLock(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	// Start scan A.
	scanA, err := q.StartScan(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan A: %v", err)
	}

	job := &StackScan{ScanID: scanA.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deq := dequeueStackScan(t, q)

	// Simulate: scan A's lock expires and scan B acquires the lock.
	q.client.Del(ctx, keyLockPrefix+"repo")
	q.client.Del(ctx, keyScanRepo+"repo")
	scanB, err := q.StartScan(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan B: %v", err)
	}

	// Complete scan A's stack scan — this triggers auto-finish via the Lua script.
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Scan A should be completed.
	finalA := getScan(t, q, scanA.ID)
	if finalA.Status != ScanStatusCompleted {
		t.Fatalf("expected scan A completed, got %s", finalA.Status)
	}

	// Scan B's lock should still be held (the Lua compare-and-delete should have skipped it).
	lockVal, err := q.client.Get(ctx, keyLockPrefix+"repo").Result()
	if err != nil {
		t.Fatalf("get lock: %v", err)
	}
	if lockVal != scanB.ID {
		t.Fatalf("expected lock owned by scan B (%s), got %s", scanB.ID, lockVal)
	}
}

func TestDriftedCounterOnCompletion(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 3)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	jobs := []*StackScan{
		{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0},
		{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/staging", MaxRetries: 0},
		{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/prod", MaxRetries: 0},
	}
	for _, j := range jobs {
		if err := q.Enqueue(ctx, j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	// Complete first two with drift, third without.
	for i := 0; i < 3; i++ {
		deq := dequeueStackScan(t, q)
		drifted := i < 2
		if err := q.Complete(ctx, deq, drifted); err != nil {
			t.Fatalf("complete %d: %v", i, err)
		}
	}

	final := getScan(t, q, scan.ID)
	if final.Drifted != 2 {
		t.Fatalf("expected drifted=2, got %d", final.Drifted)
	}
	if final.Completed != 3 {
		t.Fatalf("expected completed=3, got %d", final.Completed)
	}
	if final.Status != ScanStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
}

func TestRetryCounterTransitions(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	job := &StackScan{ScanID: scan.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 1}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Dequeue -> fail (triggers retry since MaxRetries=1).
	deq := dequeueStackScan(t, q)
	state := getScan(t, q, scan.ID)
	if state.Running != 1 || state.Queued != 0 {
		t.Fatalf("after dequeue: running=%d queued=%d", state.Running, state.Queued)
	}

	if err := q.Fail(ctx, deq, "transient"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	// After retry: running should go back down, queued back up.
	state = getScan(t, q, scan.ID)
	if state.Running != 0 || state.Queued != 1 {
		t.Fatalf("after retry: running=%d queued=%d", state.Running, state.Queued)
	}
	// Scan should still be running (not auto-finished).
	if state.Status != ScanStatusRunning {
		t.Fatalf("expected running, got %s", state.Status)
	}

	// Dequeue the retried job and complete it.
	retry := dequeueStackScan(t, q)
	if err := q.Complete(ctx, retry, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	final := getScan(t, q, scan.ID)
	if final.Status != ScanStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
}

func TestAllStacksFailAutoFinishesAsFailed(t *testing.T) {
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
	for _, j := range jobs {
		if err := q.Enqueue(ctx, j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	for i := 0; i < 2; i++ {
		deq := dequeueStackScan(t, q)
		if err := q.Fail(ctx, deq, "boom"); err != nil {
			t.Fatalf("fail %d: %v", i, err)
		}
	}

	final := getScan(t, q, scan.ID)
	if final.Status != ScanStatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
	if final.Failed != 2 || final.Errored != 2 {
		t.Fatalf("expected failed=2 errored=2, got failed=%d errored=%d", final.Failed, final.Errored)
	}
}
