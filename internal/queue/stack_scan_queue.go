package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyClaimPrefix = "driftd:claim:"

// dequeueClaimScript atomically reads a stack scan, checks its status is "pending",
// and attempts to SET NX EX the claim key. If the claim fails or the status isn't
// pending, the ID is pushed back to the queue. Returns:
//
//	 1 = claimed successfully
//	 0 = re-pushed to queue (claim failed or not pending)
//	-1 = scan data missing (caller should skip)
var dequeueClaimScript = redis.NewScript(`
local scan_data = redis.call('GET', KEYS[1])
if not scan_data then
  return -1
end

local scan = cjson.decode(scan_data)
if scan['status'] ~= 'pending' then
  redis.call('LPUSH', KEYS[3], ARGV[1])
  return 0
end

local claimed = redis.call('SET', KEYS[2], ARGV[2], 'NX', 'EX', ARGV[3])
if not claimed then
  redis.call('LPUSH', KEYS[3], ARGV[1])
  return 0
end

return 1
`)

type StackScan struct {
	ID          string    `json:"id"`
	ScanID      string    `json:"scan_id"`
	ProjectName string    `json:"project_name"`
	ProjectURL  string    `json:"project_url"`
	StackPath   string    `json:"stack_path"`
	Status      string    `json:"status"`
	Retries     int       `json:"retries"`
	MaxRetries  int       `json:"max_retries"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	WorkerID    string    `json:"worker_id,omitempty"`
	Error       string    `json:"error,omitempty"`

	Trigger string `json:"trigger,omitempty"` // "scheduled", "manual", "post-apply"
	Commit  string `json:"commit,omitempty"`
	Actor   string `json:"actor,omitempty"`
}

// ErrAlreadyClaimed is returned when another worker has already claimed the stack scan.
var ErrAlreadyClaimed = errors.New("stack scan already claimed")

var enqueueStackScanScript = redis.NewScript(`
if redis.call('SETNX', KEYS[1], ARGV[1]) == 0 then
  return 0
end
redis.call('EXPIRE', KEYS[1], ARGV[2])

local ok, err = pcall(function()
  redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[2])
  redis.call('SADD', KEYS[3], ARGV[1])
  redis.call('ZADD', KEYS[4], ARGV[4], ARGV[1])
  redis.call('SADD', KEYS[5], ARGV[1])
  if ARGV[5] ~= '' then
    redis.call('SADD', KEYS[6], ARGV[1])
  end
  redis.call('LPUSH', KEYS[7], ARGV[1])
end)

if not ok then
  redis.pcall('DEL', KEYS[1])
  redis.pcall('DEL', KEYS[2])
  redis.pcall('SREM', KEYS[3], ARGV[1])
  redis.pcall('ZREM', KEYS[4], ARGV[1])
  redis.pcall('SREM', KEYS[5], ARGV[1])
  if ARGV[5] ~= '' then
    redis.pcall('SREM', KEYS[6], ARGV[1])
  end
  redis.pcall('LREM', KEYS[7], 0, ARGV[1])
  return redis.error_reply(err)
end

return 1
`)

func (q *Queue) CancelStackScan(ctx context.Context, stackScan *StackScan, reason string) error {
	stackScan.Status = StatusCanceled
	stackScan.CompletedAt = time.Now()
	stackScan.Error = reason
	if err := q.saveStackScan(ctx, stackScan); err != nil {
		return err
	}
	if err := q.client.ZRem(ctx, keyRunningStackScans, stackScan.ID).Err(); err != nil {
		return err
	}
	q.client.Del(ctx, inflightKey(stackScan.ProjectName, stackScan.StackPath))
	return q.removeStackScanRefs(ctx, stackScan)
}

// Enqueue adds a stack scan to the queue.
func (q *Queue) Enqueue(ctx context.Context, stackScan *StackScan) error {
	stackScan.Status = StatusPending
	stackScan.CreatedAt = time.Now()
	if stackScan.ID == "" {
		stackScan.ID = fmt.Sprintf("%s:%s:%d:%d", stackScan.ProjectName, stackScan.StackPath, stackScan.CreatedAt.UnixNano(), rand.Int31())
	}

	enqueued, err := q.enqueueStackScanAtomic(ctx, stackScan)
	if err != nil {
		return err
	}
	if !enqueued {
		return ErrStackScanInflight
	}
	return nil
}

// EnqueueBatchResult holds the outcome of a batch enqueue operation.
type EnqueueBatchResult struct {
	Enqueued []*StackScan // successfully enqueued
	Skipped  int          // skipped because already inflight
	Errors   []string     // per-stack error messages
}

// EnqueueBatch enqueues multiple stack scans using atomic per-stack Redis scripts.
// Each stack enqueue is lock+write atomic, so partial enqueue cleanup races are avoided.
func (q *Queue) EnqueueBatch(ctx context.Context, stacks []*StackScan) (*EnqueueBatchResult, error) {
	if len(stacks) == 0 {
		return &EnqueueBatchResult{}, nil
	}

	now := time.Now()
	for _, ss := range stacks {
		ss.Status = StatusPending
		ss.CreatedAt = now
		if ss.ID == "" {
			ss.ID = fmt.Sprintf("%s:%s:%d:%d", ss.ProjectName, ss.StackPath, now.UnixNano(), rand.Int31())
		}
	}

	result := &EnqueueBatchResult{}
	for _, ss := range stacks {
		enqueued, err := q.enqueueStackScanAtomic(ctx, ss)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", ss.StackPath, err))
			continue
		}
		if !enqueued {
			result.Skipped++
			continue
		}
		result.Enqueued = append(result.Enqueued, ss)
	}
	return result, nil
}

func (q *Queue) enqueueStackScanAtomic(ctx context.Context, stackScan *StackScan) (bool, error) {
	stackScanData, err := json.Marshal(stackScan)
	if err != nil {
		return false, fmt.Errorf("failed to marshal stack scan: %w", err)
	}

	inflight := inflightKey(stackScan.ProjectName, stackScan.StackPath)
	stackScanKey := keyStackScanPrefix + stackScan.ID
	projectSetKey := keyProjectStackScans + stackScan.ProjectName
	projectZSetKey := keyProjectStackScansOrdered + stackScan.ProjectName
	pendingSetKey := keyStackScanPending
	scanSetKey := keyScanStackScans + stackScan.ScanID

	retentionSeconds := int64(stackScanRetention / time.Second)
	if retentionSeconds <= 0 {
		retentionSeconds = 1
	}

	result, err := enqueueStackScanScript.Run(
		ctx,
		q.client,
		[]string{
			inflight,
			stackScanKey,
			projectSetKey,
			projectZSetKey,
			pendingSetKey,
			scanSetKey,
			keyQueue,
		},
		stackScan.ID,
		strconv.FormatInt(retentionSeconds, 10),
		string(stackScanData),
		strconv.FormatInt(stackScan.CreatedAt.Unix(), 10),
		stackScan.ScanID,
	).Int64()
	if err != nil {
		return false, fmt.Errorf("failed to enqueue stack scan: %w", err)
	}

	switch result {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("unexpected enqueue result: %d", result)
	}
}

// Dequeue blocks until a stack scan is available, then returns it.
// The stack scan is atomically claimed via a Lua script that guarantees the item
// is pushed back to the queue if the claim fails, preventing items from being
// stranded in the pending set.
func (q *Queue) Dequeue(ctx context.Context, workerID string) (*StackScan, error) {
	for {
		result, err := q.client.BRPop(ctx, time.Second, keyQueue).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to dequeue: %w", err)
		}

		stackScanID := result[1]
		stackScanKey := keyStackScanPrefix + stackScanID
		claimKey := keyClaimPrefix + stackScanID

		// Use a background context for the claim script and safety pushes so
		// that a canceled dequeue context cannot strand items outside the queue.
		claimCtx := context.Background()

		claimResult, err := dequeueClaimScript.Run(
			claimCtx,
			q.client,
			[]string{stackScanKey, claimKey, keyQueue},
			stackScanID,
			workerID,
			strconv.Itoa(30*60), // 30 minutes in seconds
		).Int64()
		if err != nil {
			// Lua script error — push ID back so it isn't lost.
			_ = q.client.LPush(claimCtx, keyQueue, stackScanID).Err()
			continue
		}

		switch claimResult {
		case -1: // scan data missing
			continue
		case 0: // re-pushed by Lua (claim failed or not pending)
			continue
		case 1: // claimed
			stackScan, err := q.GetStackScan(claimCtx, stackScanID)
			if err != nil {
				_ = q.client.Del(claimCtx, claimKey).Err()
				continue
			}
			if err := q.markRunningAfterClaim(claimCtx, stackScan, workerID); err != nil {
				_ = q.client.Del(claimCtx, claimKey).Err()
				_ = q.client.LPush(claimCtx, keyQueue, stackScanID).Err()
				continue
			}
			return stackScan, nil
		default:
			continue
		}
	}
}

// markRunningAfterClaim transitions a stack scan to running after the claim key
// has already been set by the Lua script. This is the second half of the
// claim-and-mark-running operation.
func (q *Queue) markRunningAfterClaim(ctx context.Context, stackScan *StackScan, workerID string) error {
	stackScan.Status = StatusRunning
	stackScan.StartedAt = time.Now()
	stackScan.WorkerID = workerID
	if err := q.saveStackScan(ctx, stackScan); err != nil {
		return err
	}
	_ = q.client.SRem(ctx, keyStackScanPending, stackScan.ID).Err()
	if err := q.client.ZAdd(ctx, keyRunningStackScans, redis.Z{
		Score:  float64(stackScan.StartedAt.Unix()),
		Member: stackScan.ID,
	}).Err(); err != nil {
		return err
	}
	if stackScan.ScanID != "" {
		if err := q.markScanStackScanRunning(ctx, stackScan.ScanID); err != nil {
			return err
		}
	}
	return nil
}

// claimAndMarkRunning atomically claims a stack scan via SetNX, then marks it running.
// Returns ErrAlreadyClaimed if another worker already claimed it.
func (q *Queue) claimAndMarkRunning(ctx context.Context, stackScan *StackScan, workerID string) (err error) {
	if stackScan.Status != StatusPending {
		return ErrAlreadyClaimed
	}

	claimKey := keyClaimPrefix + stackScan.ID
	claimed, err := q.client.SetNX(ctx, claimKey, workerID, 30*time.Minute).Result()
	if err != nil {
		return fmt.Errorf("failed to claim stack scan: %w", err)
	}
	if !claimed {
		return ErrAlreadyClaimed
	}
	cleanupClaim := true
	defer func() {
		if cleanupClaim && err != nil {
			_ = q.client.Del(ctx, claimKey).Err()
		}
	}()

	stackScan.Status = StatusRunning
	stackScan.StartedAt = time.Now()
	stackScan.WorkerID = workerID
	if err = q.saveStackScan(ctx, stackScan); err != nil {
		return err
	}
	_ = q.client.SRem(ctx, keyStackScanPending, stackScan.ID).Err()
	if err = q.client.ZAdd(ctx, keyRunningStackScans, redis.Z{
		Score:  float64(stackScan.StartedAt.Unix()),
		Member: stackScan.ID,
	}).Err(); err != nil {
		return err
	}
	if stackScan.ScanID != "" {
		if err = q.markScanStackScanRunning(ctx, stackScan.ScanID); err != nil {
			return err
		}
	}
	cleanupClaim = false
	return nil
}

// RecoverOrphanedStackScans finds stack scans with status "pending" that are
// no longer in the queue list (e.g. lost during a crash) and re-queues them.
// This should be called periodically, not on the dequeue hot path.
func (q *Queue) RecoverOrphanedStackScans(ctx context.Context) (int, error) {
	var cursor uint64
	recovered := 0
	for {
		ids, next, err := q.client.SScan(ctx, keyStackScanPending, cursor, "*", 200).Result()
		if err != nil {
			return recovered, err
		}
		for _, id := range ids {
			stackScan, err := q.GetStackScan(ctx, id)
			if err != nil {
				_ = q.client.SRem(ctx, keyStackScanPending, id).Err()
				continue
			}
			if stackScan.Status != StatusPending {
				_ = q.client.SRem(ctx, keyStackScanPending, id).Err()
				continue
			}
			_ = q.client.SetNX(ctx, inflightKey(stackScan.ProjectName, stackScan.StackPath), stackScan.ID, stackScanRetention).Err()
			if err := q.client.LPush(ctx, keyQueue, stackScan.ID).Err(); err != nil {
				continue
			}
			recovered++
		}
		if next == 0 {
			return recovered, nil
		}
		cursor = next
	}
}

// Complete marks a stack scan as completed and releases the project lock.
func (q *Queue) Complete(ctx context.Context, stackScan *StackScan, drifted bool) error {
	stackScan.Status = StatusCompleted
	stackScan.CompletedAt = time.Now()
	if err := q.saveStackScan(ctx, stackScan); err != nil {
		return err
	}
	q.client.Del(ctx, keyClaimPrefix+stackScan.ID)
	q.client.Del(ctx, inflightKey(stackScan.ProjectName, stackScan.StackPath))
	q.client.SRem(ctx, keyStackScanPending, stackScan.ID)
	if err := q.removeStackScanRefs(ctx, stackScan); err != nil {
		return err
	}
	if stackScan.ScanID != "" {
		return q.markScanStackScanCompleted(ctx, stackScan.ScanID, drifted)
	}
	return nil
}

// Fail marks a stack scan as failed. If retries remain, re-queues it.
func (q *Queue) Fail(ctx context.Context, stackScan *StackScan, errMsg string) error {
	stackScan.Error = errMsg
	stackScan.Retries++

	if stackScan.Retries <= stackScan.MaxRetries {
		stackScan.Status = StatusPending
		stackScan.StartedAt = time.Time{}
		stackScan.WorkerID = ""
		if err := q.saveStackScan(ctx, stackScan); err != nil {
			return err
		}
		// Delete claim key so the retry can be claimed by a worker
		q.client.Del(ctx, keyClaimPrefix+stackScan.ID)
		q.client.SAdd(ctx, keyStackScanPending, stackScan.ID)
		if err := q.client.ZRem(ctx, keyRunningStackScans, stackScan.ID).Err(); err != nil {
			return err
		}
		if stackScan.ScanID != "" {
			if err := q.markScanStackScanRetry(ctx, stackScan.ScanID); err != nil {
				return err
			}
		}
		return q.client.LPush(ctx, keyQueue, stackScan.ID).Err()
	}

	stackScan.Status = StatusFailed
	stackScan.CompletedAt = time.Now()
	if err := q.saveStackScan(ctx, stackScan); err != nil {
		return err
	}
	q.client.Del(ctx, keyClaimPrefix+stackScan.ID)
	q.client.Del(ctx, inflightKey(stackScan.ProjectName, stackScan.StackPath))
	q.client.SRem(ctx, keyStackScanPending, stackScan.ID)
	if err := q.client.ZRem(ctx, keyRunningStackScans, stackScan.ID).Err(); err != nil {
		return err
	}
	if err := q.removeStackScanRefs(ctx, stackScan); err != nil {
		return err
	}
	if stackScan.ScanID != "" {
		return q.markScanStackScanFailed(ctx, stackScan.ScanID)
	}
	return nil
}

func inflightKey(projectName, stackPath string) string {
	if stackPath == "" {
		return keyStackScanInflight + projectName
	}
	return keyStackScanInflight + projectName + ":" + safeStackKey(stackPath)
}

func safeStackKey(stackPath string) string {
	return strings.ReplaceAll(stackPath, "/", "__")
}
