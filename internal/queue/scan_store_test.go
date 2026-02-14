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

		scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
		if err != nil {
			t.Fatalf("start scan: %v", err)
		}

		job := &StackScan{
			ScanID:      scan.ID,
			ProjectName: "project",
			ProjectURL:  "file:///project",
			StackPath:   "envs/dev",
			MaxRetries:  0,
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

		scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
		if err != nil {
			t.Fatalf("start scan: %v", err)
		}

		job := &StackScan{
			ScanID:      scan.ID,
			ProjectName: "project",
			ProjectURL:  "file:///project",
			StackPath:   "envs/dev",
			MaxRetries:  0,
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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	jobs := []*StackScan{
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0},
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/prod", MaxRetries: 0},
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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if got := getScan(t, q, scan.ID); got.Queued != 2 || got.Running != 0 {
		t.Fatalf("unexpected initial counts: queued=%d running=%d", got.Queued, got.Running)
	}

	jobs := []*StackScan{
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0},
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/prod", MaxRetries: 0},
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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if err := q.CancelScan(ctx, scan.ID, "project", "user canceled"); err != nil {
		t.Fatalf("cancel scan: %v", err)
	}

	canceled := getScan(t, q, scan.ID)
	if canceled.Status != ScanStatusCanceled {
		t.Fatalf("expected canceled, got %s", canceled.Status)
	}
	if canceled.Error != "user canceled" {
		t.Fatalf("expected cancel reason, got %q", canceled.Error)
	}

	if _, err := q.GetActiveScan(ctx, "project"); err != ErrScanNotFound {
		t.Fatalf("expected no active scan, got %v", err)
	}

	locked, err := q.IsProjectLocked(ctx, "project")
	if err != nil {
		t.Fatalf("is locked: %v", err)
	}
	if locked {
		t.Fatalf("expected project unlocked")
	}
}

func TestGetLastScan(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	job := &StackScan{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deq := dequeueStackScan(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	last, err := q.GetLastScan(ctx, "project")
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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 0)
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
	if final.EndedAt.IsZero() {
		t.Fatal("expected ended_at to be set")
	}
	locked, _ := q.IsProjectLocked(ctx, "project")
	if locked {
		t.Fatal("expected project unlocked after zero-stack scan")
	}
}

func TestFailScanDoesNotDeleteOtherScansLock(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scanA, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan A: %v", err)
	}

	if err := q.client.Del(ctx, keyLockPrefix+"project").Err(); err != nil {
		t.Fatalf("simulate lock expiry: %v", err)
	}
	if err := q.client.Del(ctx, keyScanRepo+"project").Err(); err != nil {
		t.Fatalf("cleanup active scan pointer: %v", err)
	}

	scanB, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan B: %v", err)
	}

	if err := q.FailScan(ctx, scanA.ID, "project", "timed out"); err != nil {
		t.Fatalf("fail scan A: %v", err)
	}

	lockVal, err := q.client.Get(ctx, keyLockPrefix+"project").Result()
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

	scanA, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan A: %v", err)
	}

	q.client.Del(ctx, keyLockPrefix+"project")
	q.client.Del(ctx, keyScanRepo+"project")

	scanB, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan B: %v", err)
	}

	if err := q.CancelScan(ctx, scanA.ID, "project", "stale"); err != nil {
		t.Fatalf("cancel scan A: %v", err)
	}

	lockVal, err := q.client.Get(ctx, keyLockPrefix+"project").Result()
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

	q.client.Set(ctx, keyLockPrefix+"project", "scan-1", time.Minute)

	if err := q.releaseOwnedLock(ctx, "project", "scan-2"); err != nil {
		t.Fatalf("releaseOwnedLock: %v", err)
	}
	locked, _ := q.IsProjectLocked(ctx, "project")
	if !locked {
		t.Fatal("expected lock to still be held after wrong-owner release")
	}

	if err := q.releaseOwnedLock(ctx, "project", "scan-1"); err != nil {
		t.Fatalf("releaseOwnedLock: %v", err)
	}
	locked, _ = q.IsProjectLocked(ctx, "project")
	if locked {
		t.Fatal("expected lock to be released after correct-owner release")
	}
}

func TestReleaseOwnedLockNoopWhenNoLock(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.releaseOwnedLock(ctx, "project", "scan-1"); err != nil {
		t.Fatalf("releaseOwnedLock on missing lock: %v", err)
	}
}

func TestMarkScanEnqueueFailed(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

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
	if state.Status != ScanStatusRunning {
		t.Fatalf("expected running, got %s", state.Status)
	}

	job := &StackScan{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deq := dequeueStackScan(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	final := getScan(t, q, scan.ID)
	if final.Status != ScanStatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
}

func TestMarkScanEnqueueFailedAutoFinishes(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
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

	locked, _ := q.IsProjectLocked(ctx, "project")
	if locked {
		t.Fatal("expected project unlocked after auto-finish")
	}
	last, err := q.GetLastScan(ctx, "project")
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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	job := &StackScan{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0}
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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	locked, _ := q.IsProjectLocked(ctx, "project")
	if !locked {
		t.Fatal("expected project locked after start")
	}

	job := &StackScan{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deq := dequeueStackScan(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	locked, _ = q.IsProjectLocked(ctx, "project")
	if locked {
		t.Fatal("expected project unlocked after auto-finish")
	}

	if _, err := q.GetActiveScan(ctx, "project"); err != ErrScanNotFound {
		t.Fatalf("expected no active scan, got %v", err)
	}

	last, err := q.GetLastScan(ctx, "project")
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

	scanA, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan A: %v", err)
	}

	job := &StackScan{ScanID: scanA.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deq := dequeueStackScan(t, q)

	q.client.Del(ctx, keyLockPrefix+"project")
	q.client.Del(ctx, keyScanRepo+"project")
	scanB, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan B: %v", err)
	}

	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	finalA := getScan(t, q, scanA.ID)
	if finalA.Status != ScanStatusCompleted {
		t.Fatalf("expected scan A completed, got %s", finalA.Status)
	}

	lockVal, err := q.client.Get(ctx, keyLockPrefix+"project").Result()
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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 3)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	jobs := []*StackScan{
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0},
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/staging", MaxRetries: 0},
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/prod", MaxRetries: 0},
	}
	for _, j := range jobs {
		if err := q.Enqueue(ctx, j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	job := &StackScan{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 1}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deq := dequeueStackScan(t, q)
	state := getScan(t, q, scan.ID)
	if state.Running != 1 || state.Queued != 0 {
		t.Fatalf("after dequeue: running=%d queued=%d", state.Running, state.Queued)
	}

	if err := q.Fail(ctx, deq, "transient"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	state = getScan(t, q, scan.ID)
	if state.Running != 0 || state.Queued != 1 {
		t.Fatalf("after retry: running=%d queued=%d", state.Running, state.Queued)
	}
	if state.Status != ScanStatusRunning {
		t.Fatalf("expected running, got %s", state.Status)
	}

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

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	jobs := []*StackScan{
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/dev", MaxRetries: 0},
		{ScanID: scan.ID, ProjectName: "project", ProjectURL: "file:///project", StackPath: "envs/prod", MaxRetries: 0},
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

func TestSetScanTotal(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 10)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	state := getScan(t, q, scan.ID)
	if state.Total != 10 || state.Queued != 10 {
		t.Fatalf("initial: total=%d queued=%d", state.Total, state.Queued)
	}

	if err := q.SetScanTotal(ctx, scan.ID, 3); err != nil {
		t.Fatalf("set scan total: %v", err)
	}

	state = getScan(t, q, scan.ID)
	if state.Total != 3 {
		t.Fatalf("expected total=3, got %d", state.Total)
	}
	if state.Queued != 3 {
		t.Fatalf("expected queued=3, got %d", state.Queued)
	}
}

func TestRenewScanLockStopsOnContextCancel(t *testing.T) {
	q := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())

	scan, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	done := make(chan struct{})
	go func() {
		q.RenewScanLock(ctx, scan.ID, "project", time.Hour, 0)
		close(done)
	}()

	// Cancel immediately — should cause RenewScanLock to return
	cancel()

	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("RenewScanLock did not stop on context cancel")
	}
}

func TestRenewScanLockLuaScriptRenewsCorrectOwner(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	lockKey := keyLockPrefix + "project"
	q.client.Set(ctx, lockKey, "scan-1", time.Minute)

	// Lua script should renew when owner matches
	renewed, err := renewLockScript.Run(ctx, q.client,
		[]string{lockKey}, "scan-1", q.lockTTL.Milliseconds(),
	).Int64()
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	if renewed != 1 {
		t.Fatalf("expected renewed=1, got %d", renewed)
	}

	// Lua script should NOT renew when owner doesn't match
	renewed, err = renewLockScript.Run(ctx, q.client,
		[]string{lockKey}, "scan-2", q.lockTTL.Milliseconds(),
	).Int64()
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	if renewed != 0 {
		t.Fatalf("expected renewed=0 for wrong owner, got %d", renewed)
	}
}

func TestRenewScanLockLuaScriptNoopWhenExpired(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	lockKey := keyLockPrefix + "project"
	// No lock set — simulates expired lock

	renewed, err := renewLockScript.Run(ctx, q.client,
		[]string{lockKey}, "scan-1", q.lockTTL.Milliseconds(),
	).Int64()
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	if renewed != 0 {
		t.Fatalf("expected renewed=0 for missing lock, got %d", renewed)
	}
}

func TestCancelAndStartScan(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	old, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	newScan, err := q.CancelAndStartScan(ctx, old.ID, "project", "superseded", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("cancel and start: %v", err)
	}
	if newScan.ID == old.ID {
		t.Fatalf("expected new scan ID")
	}

	oldState, err := q.GetScan(ctx, old.ID)
	if err != nil {
		t.Fatalf("get old scan: %v", err)
	}
	if oldState.Status != ScanStatusCanceled {
		t.Fatalf("expected old scan canceled, got %s", oldState.Status)
	}

	current, err := q.GetActiveScan(ctx, "project")
	if err != nil {
		t.Fatalf("get active scan: %v", err)
	}
	if current.ID != newScan.ID {
		t.Fatalf("expected active scan %s, got %s", newScan.ID, current.ID)
	}
}

func TestCancelAndStartScanRejectsWrongOwner(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	old, err := q.StartScan(ctx, "project", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	// Simulate lock stolen by another scan
	if err := q.client.Set(ctx, keyLockPrefix+"project", "other", time.Minute).Err(); err != nil {
		t.Fatalf("set lock: %v", err)
	}

	_, err = q.CancelAndStartScan(ctx, old.ID, "project", "superseded", "manual", "", "", 0)
	if err != ErrProjectLocked {
		t.Fatalf("expected ErrProjectLocked, got %v", err)
	}
}
