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
// KEYS: [1] scan hash key, [2] lock key, [3] scan:project: key, [4] scan:last: key, [5] running scans zset
// ARGV: [1] scan_id, [2] ended_at, [3..N] pairs of (field, delta) to apply
//
// Returns a tuple: [status, completed, failed, total, drifted, ended_at].
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
local drifted = tonumber(redis.call('HGET', key, 'drifted') or '0')
local status = redis.call('HGET', key, 'status') or 'running'
local ended = 0

if status == 'running' and ((total == 0) or (comp + fail >= total)) then
  status = 'completed'
  if fail > 0 then status = 'failed' end
  redis.call('HSET', key, 'status', status, 'ended_at', ended_at)
  ended = tonumber(ended_at)
  -- Compare-and-delete: only release lock if we still own it
  if redis.call('GET', lock_key) == scan_id then
    redis.call('DEL', lock_key)
  end
  redis.call('DEL', repo_key)
  redis.call('SET', last_key, scan_id, 'EX', 604800)
  redis.call('ZREM', running_key, scan_id)
end
return {status, comp, fail, total, drifted, ended}
`)

type scanTransitionState struct {
	Status    string
	Completed int
	Failed    int
	Total     int
	Drifted   int
	EndedAt   *time.Time
}

func (q *Queue) scanTransitionKeys(scanID, projectName string) []string {
	return []string{
		keyScanPrefix + scanID,
		keyLockPrefix + projectName,
		keyScanRepo + projectName,
		keyScanLast + projectName,
		keyRunningScans,
	}
}

func (q *Queue) runScanTransition(ctx context.Context, scanID, projectName string, deltas ...any) error {
	keys := q.scanTransitionKeys(scanID, projectName)
	args := []any{scanID, time.Now().Unix()}
	args = append(args, deltas...)
	result, err := scanTransitionScript.Run(ctx, q.client, keys, args...).Result()
	if err != nil {
		return err
	}
	state, err := parseScanTransitionState(result)
	if err != nil {
		return err
	}
	q.publishScanUpdateFromState(ctx, scanID, projectName, state)
	return nil
}

func (q *Queue) projectNameForScan(ctx context.Context, scanID string) (string, error) {
	project, err := q.client.HGet(ctx, keyScanPrefix+scanID, "project").Result()
	if err != nil {
		return "", fmt.Errorf("failed to get project for scan %s: %w", scanID, err)
	}
	return project, nil
}

func (q *Queue) markScanStackScanRunning(ctx context.Context, scanID string) error {
	projectName, err := q.projectNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, projectName, "running", 1, "queued", -1)
}

func (q *Queue) markScanStackScanRetry(ctx context.Context, scanID string) error {
	projectName, err := q.projectNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, projectName, "running", -1, "queued", 1)
}

func (q *Queue) markScanStackScanCompleted(ctx context.Context, scanID string, drifted bool) error {
	projectName, err := q.projectNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	deltas := []any{"running", -1, "completed", 1}
	if drifted {
		deltas = append(deltas, "drifted", 1)
	}
	return q.runScanTransition(ctx, scanID, projectName, deltas...)
}

func (q *Queue) markScanStackScanFailed(ctx context.Context, scanID string) error {
	projectName, err := q.projectNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, projectName, "running", -1, "failed", 1, "errored", 1)
}

// AdjustScanCounters atomically updates scan counters and auto-finishes the scan
// if all stacks are done. Use this when you know the projectName and want to apply
// multiple counter deltas in a single call (e.g. batch enqueue skips/failures).
// Deltas are pairs of (field, delta): "queued", -3, "total", -3
func (q *Queue) AdjustScanCounters(ctx context.Context, scanID, projectName string, deltas ...any) error {
	return q.runScanTransition(ctx, scanID, projectName, deltas...)
}

func (q *Queue) MarkScanEnqueueFailed(ctx context.Context, scanID string) error {
	projectName, err := q.projectNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, projectName, "queued", -1, "failed", 1, "errored", 1)
}

func (q *Queue) MarkScanEnqueueSkipped(ctx context.Context, scanID string) error {
	projectName, err := q.projectNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, projectName, "queued", -1, "total", -1)
}

func (q *Queue) publishScanUpdateFromState(ctx context.Context, scanID, projectName string, state scanTransitionState) {
	_ = q.PublishScanEvent(ctx, projectName, ScanEvent{
		ProjectName: projectName,
		ScanID:      scanID,
		Status:      state.Status,
		Completed:   state.Completed,
		Failed:      state.Failed,
		Total:       state.Total,
		DriftedCnt:  state.Drifted,
		EndedAt:     state.EndedAt,
	})
}

func parseScanTransitionState(result any) (scanTransitionState, error) {
	arr, ok := result.([]interface{})
	if !ok || len(arr) < 6 {
		return scanTransitionState{}, fmt.Errorf("unexpected scan transition result: %v", result)
	}

	state := scanTransitionState{
		Status:    fmt.Sprintf("%v", arr[0]),
		Completed: toInt(arr[1]),
		Failed:    toInt(arr[2]),
		Total:     toInt(arr[3]),
		Drifted:   toInt(arr[4]),
	}
	if ended := toInt64(arr[5]); ended > 0 {
		endedAt := time.Unix(ended, 0)
		state.EndedAt = &endedAt
	}
	return state, nil
}
