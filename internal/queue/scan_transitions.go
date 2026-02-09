package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// scanTransitionScript is the core Lua script for atomically updating scan counters
// and auto-finishing the scan when all stack scans are done. It also performs
// compare-and-delete on the lock key to avoid releasing another scan's lock.
//
// KEYS: [1] scan hash key, [2] lock key, [3] scan:repo: key, [4] scan:last: key, [5] running scans zset
// ARGV: [1] scan_id, [2] ended_at, [3..N] pairs of (field, delta) to apply
//
// Returns 1 if the scan was auto-finished, 0 otherwise.
var scanTransitionScript = redis.NewScript(`
local key = KEYS[1]
local lock_key = KEYS[2]
local repo_key = KEYS[3]
local last_key = KEYS[4]
local running_key = KEYS[5]
local scan_id = ARGV[1]
local ended_at = ARGV[2]

-- Apply counter deltas (pairs of field, delta starting at ARGV[3])
for i = 3, #ARGV, 2 do
  local field = ARGV[i]
  local delta = tonumber(ARGV[i+1])
  local val = redis.call('HINCRBY', key, field, delta)
  if val < 0 then
    redis.call('HSET', key, field, 0)
  end
end

-- Check if scan should auto-finish
local total = tonumber(redis.call('HGET', key, 'total') or '0')
local comp  = tonumber(redis.call('HGET', key, 'completed') or '0')
local fail  = tonumber(redis.call('HGET', key, 'failed') or '0')

if (total == 0) or (comp + fail >= total) then
  local status = 'completed'
  if fail > 0 then status = 'failed' end
  redis.call('HSET', key, 'status', status, 'ended_at', ended_at)
  -- Compare-and-delete: only release lock if we still own it
  if redis.call('GET', lock_key) == scan_id then
    redis.call('DEL', lock_key)
  end
  redis.call('DEL', repo_key)
  redis.call('SET', last_key, scan_id, 'EX', 604800)
  redis.call('ZREM', running_key, scan_id)
  return 1
end
return 0
`)

func (q *Queue) scanTransitionKeys(scanID, repoName string) []string {
	return []string{
		keyScanPrefix + scanID,
		keyLockPrefix + repoName,
		keyScanRepo + repoName,
		keyScanLast + repoName,
		keyRunningScans,
	}
}

func (q *Queue) runScanTransition(ctx context.Context, scanID, repoName string, deltas ...any) error {
	keys := q.scanTransitionKeys(scanID, repoName)
	args := []any{scanID, time.Now().Unix()}
	args = append(args, deltas...)
	if err := scanTransitionScript.Run(ctx, q.client, keys, args...).Err(); err != nil {
		return err
	}
	q.publishScanUpdate(ctx, scanID, repoName)
	return nil
}

func (q *Queue) repoNameForScan(ctx context.Context, scanID string) (string, error) {
	repo, err := q.client.HGet(ctx, keyScanPrefix+scanID, "repo").Result()
	if err != nil {
		return "", fmt.Errorf("failed to get repo for scan %s: %w", scanID, err)
	}
	return repo, nil
}

func (q *Queue) markScanStackScanRunning(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "running", 1, "queued", -1)
}

func (q *Queue) markScanStackScanRetry(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "running", -1, "queued", 1)
}

func (q *Queue) markScanStackScanCompleted(ctx context.Context, scanID string, drifted bool) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	deltas := []any{"running", -1, "completed", 1}
	if drifted {
		deltas = append(deltas, "drifted", 1)
	}
	return q.runScanTransition(ctx, scanID, repoName, deltas...)
}

func (q *Queue) markScanStackScanFailed(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "running", -1, "failed", 1, "errored", 1)
}

// AdjustScanCounters atomically updates scan counters and auto-finishes the scan
// if all stacks are done. Use this when you know the repoName and want to apply
// multiple counter deltas in a single call (e.g. batch enqueue skips/failures).
// Deltas are pairs of (field, delta): "queued", -3, "total", -3
func (q *Queue) AdjustScanCounters(ctx context.Context, scanID, repoName string, deltas ...any) error {
	return q.runScanTransition(ctx, scanID, repoName, deltas...)
}

func (q *Queue) MarkScanEnqueueFailed(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "queued", -1, "failed", 1, "errored", 1)
}

func (q *Queue) MarkScanEnqueueSkipped(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "queued", -1, "total", -1)
}

func (q *Queue) publishScanUpdate(ctx context.Context, scanID, repoName string) {
	scan, err := q.GetScan(ctx, scanID)
	if err != nil {
		return
	}
	var endedAt *time.Time
	if !scan.EndedAt.IsZero() {
		endedAt = &scan.EndedAt
	}
	_ = q.PublishScanEvent(ctx, repoName, ScanEvent{
		RepoName:  repoName,
		ScanID:    scanID,
		Status:    scan.Status,
		Completed: scan.Completed,
		Failed:    scan.Failed,
		Total:     scan.Total,
		EndedAt:   endedAt,
	})
}
