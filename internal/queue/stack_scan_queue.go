package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyClaimPrefix = "driftd:claim:"

type StackScan struct {
	ID          string    `json:"id"`
	ScanID      string    `json:"scan_id"`
	RepoName    string    `json:"repo_name"`
	RepoURL     string    `json:"repo_url"`
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
	q.client.Del(ctx, inflightKey(stackScan.RepoName, stackScan.StackPath))
	return q.removeStackScanRefs(ctx, stackScan)
}

// Enqueue adds a stack scan to the queue.
func (q *Queue) Enqueue(ctx context.Context, stackScan *StackScan) error {
	stackScan.Status = StatusPending
	stackScan.CreatedAt = time.Now()
	if stackScan.ID == "" {
		stackScan.ID = fmt.Sprintf("%s:%s:%d:%d", stackScan.RepoName, stackScan.StackPath, stackScan.CreatedAt.UnixNano(), rand.Int31())
	}

	inflight := inflightKey(stackScan.RepoName, stackScan.StackPath)
	claimed, err := q.client.SetNX(ctx, inflight, stackScan.ID, stackScanRetention).Result()
	if err != nil {
		return fmt.Errorf("failed to mark stack scan inflight: %w", err)
	}
	if !claimed {
		return ErrStackScanInflight
	}

	stackScanKey := keyStackScanPrefix + stackScan.ID
	stackScanData, err := json.Marshal(stackScan)
	if err != nil {
		q.client.Del(ctx, inflight)
		return fmt.Errorf("failed to marshal stack scan: %w", err)
	}

	pipe := q.client.Pipeline()
	pipe.Set(ctx, stackScanKey, stackScanData, stackScanRetention)
	pipe.SAdd(ctx, keyRepoStackScans+stackScan.RepoName, stackScan.ID)
	pipe.ZAdd(ctx, keyRepoStackScansOrdered+stackScan.RepoName, redis.Z{
		Score:  float64(stackScan.CreatedAt.Unix()),
		Member: stackScan.ID,
	})
	if stackScan.ScanID != "" {
		pipe.SAdd(ctx, keyScanStackScans+stackScan.ScanID, stackScan.ID)
	}
	pipe.LPush(ctx, keyQueue, stackScan.ID)

	if _, err := pipe.Exec(ctx); err != nil {
		q.client.Del(ctx, inflight)
		return fmt.Errorf("failed to enqueue stack scan: %w", err)
	}

	return nil
}

// Dequeue blocks until a stack scan is available, then returns it.
// The stack scan status is atomically claimed and updated to "running".
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
		stackScan, err := q.GetStackScan(ctx, stackScanID)
		if err != nil {
			continue
		}

		if err := q.claimAndMarkRunning(ctx, stackScan, workerID); err != nil {
			if errors.Is(err, ErrAlreadyClaimed) {
				continue
			}
			return nil, err
		}

		return stackScan, nil
	}
}

// claimAndMarkRunning atomically claims a stack scan via SetNX, then marks it running.
// Returns ErrAlreadyClaimed if another worker already claimed it.
func (q *Queue) claimAndMarkRunning(ctx context.Context, stackScan *StackScan, workerID string) error {
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

	stackScan.Status = StatusRunning
	stackScan.StartedAt = time.Now()
	stackScan.WorkerID = workerID
	if err := q.saveStackScan(ctx, stackScan); err != nil {
		return err
	}
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

// RecoverOrphanedStackScans finds stack scans with status "pending" that are
// no longer in the queue list (e.g. lost during a crash) and re-queues them.
// This should be called periodically, not on the dequeue hot path.
func (q *Queue) RecoverOrphanedStackScans(ctx context.Context) (int, error) {
	var cursor uint64
	recovered := 0
	for {
		keys, next, err := q.client.Scan(ctx, cursor, keyStackScanPrefix+"*", 100).Result()
		if err != nil {
			return recovered, err
		}
		for _, key := range keys {
			data, err := q.client.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			var stackScan StackScan
			if err := json.Unmarshal([]byte(data), &stackScan); err != nil {
				continue
			}
			if stackScan.Status != StatusPending {
				continue
			}
			_ = q.client.SetNX(ctx, inflightKey(stackScan.RepoName, stackScan.StackPath), stackScan.ID, stackScanRetention).Err()
			// Re-push to queue list so a worker can pick it up
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

// Complete marks a stack scan as completed and releases the repo lock.
func (q *Queue) Complete(ctx context.Context, stackScan *StackScan, drifted bool) error {
	stackScan.Status = StatusCompleted
	stackScan.CompletedAt = time.Now()
	if err := q.saveStackScan(ctx, stackScan); err != nil {
		return err
	}
	q.client.Del(ctx, keyClaimPrefix+stackScan.ID)
	q.client.Del(ctx, inflightKey(stackScan.RepoName, stackScan.StackPath))
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
	q.client.Del(ctx, inflightKey(stackScan.RepoName, stackScan.StackPath))
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

func inflightKey(repoName, stackPath string) string {
	if stackPath == "" {
		return keyStackScanInflight + repoName
	}
	return keyStackScanInflight + repoName + ":" + safeStackKey(stackPath)
}

func safeStackKey(stackPath string) string {
	return strings.ReplaceAll(stackPath, "/", "__")
}
