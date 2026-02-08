package queue

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RecoverStaleStackScans finds running stack scans older than maxAge and
// marks them as failed (or re-queued if retries remain).
func (q *Queue) RecoverStaleStackScans(ctx context.Context, maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}

	cutoff := time.Now().Add(-maxAge).Unix()
	ids, err := q.client.ZRangeByScore(ctx, keyRunningStackScans, &redis.ZRangeBy{
		Min: "-inf",
		Max: strconv.FormatInt(cutoff, 10),
	}).Result()
	if err != nil {
		return 0, err
	}

	recovered := 0
	for _, id := range ids {
		stackScan, err := q.GetStackScan(ctx, id)
		if err != nil {
			_ = q.client.ZRem(ctx, keyRunningStackScans, id).Err()
			continue
		}
		if stackScan.Status != StatusRunning {
			_ = q.client.ZRem(ctx, keyRunningStackScans, id).Err()
			continue
		}
		if stackScan.StartedAt.After(time.Now().Add(-maxAge)) {
			continue
		}
		if err := q.Fail(ctx, stackScan, "stale stack scan exceeded max age"); err != nil {
			continue
		}
		recovered++
	}

	return recovered, nil
}
