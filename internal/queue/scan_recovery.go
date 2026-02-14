package queue

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RecoverStaleScans finds running scans older than maxAge and marks them failed.
func (q *Queue) RecoverStaleScans(ctx context.Context, maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}

	cutoff := time.Now().Add(-maxAge).Unix()
	ids, err := q.client.ZRangeByScore(ctx, keyRunningScans, &redis.ZRangeBy{
		Min: "-inf",
		Max: strconv.FormatInt(cutoff, 10),
	}).Result()
	if err != nil {
		return 0, err
	}

	recovered := 0
	for _, id := range ids {
		scan, err := q.GetScan(ctx, id)
		if err != nil {
			_ = q.client.ZRem(ctx, keyRunningScans, id).Err()
			continue
		}
		if scan.Status != ScanStatusRunning {
			_ = q.client.ZRem(ctx, keyRunningScans, id).Err()
			continue
		}
		if err := q.FailScan(ctx, scan.ID, scan.ProjectName, "scan exceeded maximum duration"); err != nil {
			continue
		}
		recovered++
	}

	return recovered, nil
}

// RebuildRunningScansIndex scans for scan hashes with status "running" and
// re-populates the keyRunningScans ZSET. This handles the case where the ZSET
// was lost (e.g. Redis restart without persistence) but scan hashes survived.
func (q *Queue) RebuildRunningScansIndex(ctx context.Context) (int, error) {
	var cursor uint64
	rebuilt := 0

	for {
		keys, next, err := q.client.Scan(ctx, cursor, keyScanPrefix+"*", 100).Result()
		if err != nil {
			return rebuilt, err
		}

		for _, key := range keys {
			vals, err := q.client.HMGet(ctx, key, "status", "started_at").Result()
			if err != nil || len(vals) < 2 {
				continue
			}
			status, _ := vals[0].(string)
			startedStr, _ := vals[1].(string)
			if status != ScanStatusRunning {
				continue
			}
			startedUnix, err := strconv.ParseInt(startedStr, 10, 64)
			if err != nil || startedUnix == 0 {
				continue
			}
			scanID := key[len(keyScanPrefix):]
			added, err := q.client.ZAddNX(ctx, keyRunningScans, redis.Z{
				Score:  float64(startedUnix),
				Member: scanID,
			}).Result()
			if err == nil && added > 0 {
				rebuilt++
			}
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}

	return rebuilt, nil
}
