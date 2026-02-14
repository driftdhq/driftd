package queue

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRecoverStaleStackScans(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	stackScan := &StackScan{
		ID:          "scan-stale",
		ProjectName: "project",
		StackPath:   "envs/dev",
		Status:      StatusRunning,
		StartedAt:   time.Now().Add(-2 * time.Hour),
		MaxRetries:  0,
	}

	if err := q.saveStackScan(ctx, stackScan); err != nil {
		t.Fatalf("save scan: %v", err)
	}
	if err := q.client.ZAdd(ctx, keyRunningStackScans, redis.Z{
		Score:  float64(stackScan.StartedAt.Unix()),
		Member: stackScan.ID,
	}).Err(); err != nil {
		t.Fatalf("zadd: %v", err)
	}

	recovered, err := q.RecoverStaleStackScans(ctx, time.Hour)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered, got %d", recovered)
	}

	updated, err := q.GetStackScan(ctx, stackScan.ID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	if updated.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", updated.Status)
	}

	running, err := q.client.ZCard(ctx, keyRunningStackScans).Result()
	if err != nil {
		t.Fatalf("zcard: %v", err)
	}
	if running != 0 {
		t.Fatalf("expected running set empty, got %d", running)
	}
}
