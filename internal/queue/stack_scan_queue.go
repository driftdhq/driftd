package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

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

	// Optional metadata from trigger
	Trigger string `json:"trigger,omitempty"` // "scheduled", "manual", "post-apply"
	Commit  string `json:"commit,omitempty"`
	Actor   string `json:"actor,omitempty"`
}

func (q *Queue) CancelStackScan(ctx context.Context, stackScan *StackScan, reason string) error {
	stackScan.Status = StatusCanceled
	stackScan.CompletedAt = time.Now()
	stackScan.Error = reason
	if err := q.updateStackScan(ctx, stackScan); err != nil {
		return err
	}
	if err := q.client.SRem(ctx, keyRepoStackScans+stackScan.RepoName, stackScan.ID).Err(); err != nil {
		return err
	}
	return q.client.ZRem(ctx, keyRepoStackScansOrdered+stackScan.RepoName, stackScan.ID).Err()
}

// Enqueue adds a stack scan to the queue.
func (q *Queue) Enqueue(ctx context.Context, stackScan *StackScan) error {
	// Set stack scan defaults
	stackScan.Status = StatusPending
	stackScan.CreatedAt = time.Now()
	if stackScan.ID == "" {
		stackScan.ID = fmt.Sprintf("%s:%s:%d:%d", stackScan.RepoName, stackScan.StackPath, stackScan.CreatedAt.UnixNano(), rand.Int31())
	}

	// Store stack scan
	stackScanKey := keyStackScanPrefix + stackScan.ID
	stackScanData, err := json.Marshal(stackScan)
	if err != nil {
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
		return fmt.Errorf("failed to enqueue stack scan: %w", err)
	}

	return nil
}

// Dequeue blocks until a stack scan is available, then returns it.
// The stack scan status is updated to "running" and the repo lock is acquired.
func (q *Queue) Dequeue(ctx context.Context, workerID string) (*StackScan, error) {
	for {
		// Block waiting for stack scan (1 second timeout to allow checking for orphaned stack scans)
		result, err := q.client.BRPop(ctx, time.Second, keyQueue).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				stackScan, findErr := q.findPendingStackScan(ctx)
				if findErr != nil {
					return nil, findErr
				}
				if stackScan == nil {
					continue
				}
				if err := q.markStackScanRunning(ctx, stackScan, workerID); err != nil {
					return nil, err
				}
				return stackScan, nil
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to dequeue: %w", err)
		}

		stackScanID := result[1]
		stackScan, err := q.GetStackScan(ctx, stackScanID)
		if err != nil {
			// StackScan expired or deleted, try next
			continue
		}

		if err := q.markStackScanRunning(ctx, stackScan, workerID); err != nil {
			return nil, err
		}

		return stackScan, nil
	}
}

func (q *Queue) markStackScanRunning(ctx context.Context, stackScan *StackScan, workerID string) error {
	stackScan.Status = StatusRunning
	stackScan.StartedAt = time.Now()
	stackScan.WorkerID = workerID
	if err := q.updateStackScan(ctx, stackScan); err != nil {
		return err
	}
	if stackScan.ScanID != "" {
		if err := q.markScanStackScanRunning(ctx, stackScan.ScanID); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) findPendingStackScan(ctx context.Context) (*StackScan, error) {
	var cursor uint64
	for {
		keys, next, err := q.client.Scan(ctx, cursor, keyStackScanPrefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			data, err := q.client.Get(ctx, key).Result()
			if err != nil {
				if errors.Is(err, redis.Nil) {
					continue
				}
				return nil, err
			}
			var stackScan StackScan
			if err := json.Unmarshal([]byte(data), &stackScan); err != nil {
				continue
			}
			if stackScan.Status == StatusPending {
				return &stackScan, nil
			}
		}
		if next == 0 {
			return nil, nil
		}
		cursor = next
	}
}

// Complete marks a stack scan as completed and releases the repo lock.
func (q *Queue) Complete(ctx context.Context, stackScan *StackScan, drifted bool) error {
	stackScan.Status = StatusCompleted
	stackScan.CompletedAt = time.Now()
	if err := q.updateStackScan(ctx, stackScan); err != nil {
		return err
	}
	if err := q.client.SRem(ctx, keyRepoStackScans+stackScan.RepoName, stackScan.ID).Err(); err != nil {
		return err
	}
	if err := q.client.ZRem(ctx, keyRepoStackScansOrdered+stackScan.RepoName, stackScan.ID).Err(); err != nil {
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
		// Re-queue for retry
		stackScan.Status = StatusPending
		stackScan.StartedAt = time.Time{}
		stackScan.WorkerID = ""
		if err := q.updateStackScan(ctx, stackScan); err != nil {
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
	if err := q.updateStackScan(ctx, stackScan); err != nil {
		return err
	}
	if err := q.client.SRem(ctx, keyRepoStackScans+stackScan.RepoName, stackScan.ID).Err(); err != nil {
		return err
	}
	if err := q.client.ZRem(ctx, keyRepoStackScansOrdered+stackScan.RepoName, stackScan.ID).Err(); err != nil {
		return err
	}
	if stackScan.ScanID != "" {
		return q.markScanStackScanFailed(ctx, stackScan.ScanID)
	}
	return nil
}

// GetStackScan retrieves a stack scan by ID.
func (q *Queue) GetStackScan(ctx context.Context, stackScanID string) (*StackScan, error) {
	stackScanKey := keyStackScanPrefix + stackScanID
	data, err := q.client.Get(ctx, stackScanKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrStackScanNotFound
		}
		return nil, fmt.Errorf("failed to get stack scan: %w", err)
	}

	var stackScan StackScan
	if err := json.Unmarshal([]byte(data), &stackScan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal stack scan: %w", err)
	}
	return &stackScan, nil
}

// ListRepoStackScans returns recent stack scans for a repo.
func (q *Queue) ListRepoStackScans(ctx context.Context, repoName string, limit int) ([]*StackScan, error) {
	stop := int64(-1)
	if limit > 0 {
		stop = int64(limit - 1)
	}
	stackScanIDs, err := q.client.ZRevRange(ctx, keyRepoStackScansOrdered+repoName, 0, stop).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list stack scan IDs: %w", err)
	}
	if len(stackScanIDs) == 0 {
		return nil, nil
	}

	pipe := q.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(stackScanIDs))
	for i, id := range stackScanIDs {
		cmds[i] = pipe.Get(ctx, keyStackScanPrefix+id)
	}
	_, err = pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to fetch stack scans: %w", err)
	}

	var stackScans []*StackScan
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue // StackScan expired
		}
		var stackScan StackScan
		if err := json.Unmarshal([]byte(data), &stackScan); err != nil {
			continue
		}
		stackScans = append(stackScans, &stackScan)
	}

	return stackScans, nil
}

func (q *Queue) updateStackScan(ctx context.Context, stackScan *StackScan) error {
	stackScanKey := keyStackScanPrefix + stackScan.ID
	stackScanData, err := json.Marshal(stackScan)
	if err != nil {
		return fmt.Errorf("failed to marshal stack scan: %w", err)
	}
	return q.client.Set(ctx, stackScanKey, stackScanData, stackScanRetention).Err()
}
