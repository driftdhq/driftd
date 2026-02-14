package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

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

func (q *Queue) ListProjectStackScans(ctx context.Context, projectName string, limit int) ([]*StackScan, error) {
	stop := int64(-1)
	if limit > 0 {
		stop = int64(limit - 1)
	}
	stackScanIDs, err := q.client.ZRevRange(ctx, keyProjectStackScansOrdered+projectName, 0, stop).Result()
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

func (q *Queue) saveStackScan(ctx context.Context, stackScan *StackScan) error {
	stackScanKey := keyStackScanPrefix + stackScan.ID
	stackScanData, err := json.Marshal(stackScan)
	if err != nil {
		return fmt.Errorf("failed to marshal stack scan: %w", err)
	}
	return q.client.Set(ctx, stackScanKey, stackScanData, stackScanRetention).Err()
}

func (q *Queue) removeStackScanRefs(ctx context.Context, stackScan *StackScan) error {
	if err := q.client.SRem(ctx, keyProjectStackScans+stackScan.ProjectName, stackScan.ID).Err(); err != nil {
		return err
	}
	return q.client.ZRem(ctx, keyProjectStackScansOrdered+stackScan.ProjectName, stackScan.ID).Err()
}

// ClearInflightForScan removes inflight markers for all stack scans belonging to a scan.
func (q *Queue) ClearInflightForScan(ctx context.Context, scanID string) {
	stackScanIDs, err := q.client.SMembers(ctx, keyScanStackScans+scanID).Result()
	if err != nil {
		return
	}
	for _, id := range stackScanIDs {
		stackScan, err := q.GetStackScan(ctx, id)
		if err != nil {
			continue
		}
		q.client.Del(ctx, inflightKey(stackScan.ProjectName, stackScan.StackPath))
	}
}
