package queue

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRecoverStaleScans(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	scan := &Scan{
		ID:        "scan-1",
		RepoName:  "repo",
		Status:    ScanStatusRunning,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		StartedAt: time.Now().Add(-2 * time.Hour),
	}

	if err := q.client.HSet(ctx, keyScanPrefix+scan.ID, map[string]any{
		"id":         scan.ID,
		"repo":       scan.RepoName,
		"status":     scan.Status,
		"created_at": scan.CreatedAt.Unix(),
		"started_at": scan.StartedAt.Unix(),
		"total":      1,
	}).Err(); err != nil {
		t.Fatalf("hset scan: %v", err)
	}
	if err := q.client.ZAdd(ctx, keyRunningScans, redis.Z{
		Score:  float64(scan.StartedAt.Unix()),
		Member: scan.ID,
	}).Err(); err != nil {
		t.Fatalf("zadd running scans: %v", err)
	}

	recovered, err := q.RecoverStaleScans(ctx, time.Hour)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered, got %d", recovered)
	}

	updated, err := q.GetScan(ctx, scan.ID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	if updated.Status != ScanStatusFailed {
		t.Fatalf("expected failed, got %s", updated.Status)
	}

	count, err := q.client.ZCard(ctx, keyRunningScans).Result()
	if err != nil {
		t.Fatalf("zcard: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected running scans empty, got %d", count)
	}
}

func setupScanHash(t *testing.T, q *Queue, scanID, repo, status string, startedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := q.client.HSet(ctx, keyScanPrefix+scanID, map[string]any{
		"id":         scanID,
		"repo":       repo,
		"status":     status,
		"created_at": startedAt.Unix(),
		"started_at": startedAt.Unix(),
		"total":      1,
	}).Err(); err != nil {
		t.Fatalf("hset scan %s: %v", scanID, err)
	}
}

func TestRebuildRunningScansIndex(t *testing.T) {
	t.Run("happy path reindexes running scan", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()
		startedAt := time.Now().Add(-30 * time.Minute)

		setupScanHash(t, q, "scan-1", "repo", ScanStatusRunning, startedAt)

		rebuilt, err := q.RebuildRunningScansIndex(ctx)
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if rebuilt != 1 {
			t.Fatalf("expected 1 rebuilt, got %d", rebuilt)
		}

		score, err := q.client.ZScore(ctx, keyRunningScans, "scan-1").Result()
		if err != nil {
			t.Fatalf("zscore: %v", err)
		}
		if int64(score) != startedAt.Unix() {
			t.Fatalf("expected score %d, got %d", startedAt.Unix(), int64(score))
		}
	})

	t.Run("skips non-running scans", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()
		startedAt := time.Now().Add(-30 * time.Minute)

		setupScanHash(t, q, "scan-completed", "repo", ScanStatusCompleted, startedAt)
		setupScanHash(t, q, "scan-failed", "repo", ScanStatusFailed, startedAt)

		rebuilt, err := q.RebuildRunningScansIndex(ctx)
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if rebuilt != 0 {
			t.Fatalf("expected 0 rebuilt, got %d", rebuilt)
		}

		count, err := q.client.ZCard(ctx, keyRunningScans).Result()
		if err != nil {
			t.Fatalf("zcard: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected empty ZSET, got %d", count)
		}
	})

	t.Run("idempotent via ZAddNX", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()
		startedAt := time.Now().Add(-30 * time.Minute)

		setupScanHash(t, q, "scan-1", "repo", ScanStatusRunning, startedAt)

		// Pre-populate the ZSET with a different score
		q.client.ZAdd(ctx, keyRunningScans, redis.Z{
			Score:  float64(startedAt.Add(-time.Hour).Unix()),
			Member: "scan-1",
		})

		rebuilt, err := q.RebuildRunningScansIndex(ctx)
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if rebuilt != 0 {
			t.Fatalf("expected 0 rebuilt (already indexed), got %d", rebuilt)
		}

		// Score should remain the original value, not overwritten
		score, err := q.client.ZScore(ctx, keyRunningScans, "scan-1").Result()
		if err != nil {
			t.Fatalf("zscore: %v", err)
		}
		originalScore := float64(startedAt.Add(-time.Hour).Unix())
		if score != originalScore {
			t.Fatalf("expected original score %v preserved, got %v", originalScore, score)
		}
	})

	t.Run("skips scan with zero started_at", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()

		// Manually set started_at to "0"
		q.client.HSet(ctx, keyScanPrefix+"scan-zero", map[string]any{
			"id":         "scan-zero",
			"repo":       "repo",
			"status":     ScanStatusRunning,
			"started_at": strconv.FormatInt(0, 10),
		})

		rebuilt, err := q.RebuildRunningScansIndex(ctx)
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if rebuilt != 0 {
			t.Fatalf("expected 0 rebuilt for zero started_at, got %d", rebuilt)
		}
	})
}
