package queue

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRunningScanMetrics(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	started := time.Now().Add(-30 * time.Second)
	if err := q.client.ZAdd(ctx, keyRunningScans, redis.Z{
		Score:  float64(started.Unix()),
		Member: "scan-1",
	}).Err(); err != nil {
		t.Fatalf("zadd: %v", err)
	}
	if err := q.client.ZAdd(ctx, keyRunningStackScans, redis.Z{
		Score:  float64(started.Unix()),
		Member: "stack-1",
	}).Err(); err != nil {
		t.Fatalf("zadd stack: %v", err)
	}

	count, err := q.RunningScanCount(ctx)
	if err != nil || count != 1 {
		t.Fatalf("running scans count: %v %d", err, count)
	}
	stackCount, err := q.RunningStackScanCount(ctx)
	if err != nil || stackCount != 1 {
		t.Fatalf("running stack scans count: %v %d", err, stackCount)
	}

	age, err := q.OldestRunningScanAge(ctx)
	if err != nil || age <= 0 {
		t.Fatalf("running scan age: %v %v", err, age)
	}
	stackAge, err := q.OldestRunningStackScanAge(ctx)
	if err != nil || stackAge <= 0 {
		t.Fatalf("running stack scan age: %v %v", err, stackAge)
	}
}
