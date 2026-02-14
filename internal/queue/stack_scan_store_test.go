package queue

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestStackScanStoreGetAndList(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	now := time.Now()
	first := &StackScan{
		ID:          "scan-1",
		ProjectName: "project",
		StackPath:   "envs/dev",
		Status:      StatusCompleted,
		CreatedAt:   now.Add(-time.Minute),
	}
	second := &StackScan{
		ID:          "scan-2",
		ProjectName: "project",
		StackPath:   "envs/prod",
		Status:      StatusCompleted,
		CreatedAt:   now,
	}

	for _, scan := range []*StackScan{first, second} {
		if err := q.saveStackScan(ctx, scan); err != nil {
			t.Fatalf("save scan: %v", err)
		}
		if err := q.client.SAdd(ctx, keyProjectStackScans+scan.ProjectName, scan.ID).Err(); err != nil {
			t.Fatalf("sadd: %v", err)
		}
		if err := q.client.ZAdd(ctx, keyProjectStackScansOrdered+scan.ProjectName, redis.Z{
			Score:  float64(scan.CreatedAt.Unix()),
			Member: scan.ID,
		}).Err(); err != nil {
			t.Fatalf("zadd: %v", err)
		}
	}

	got, err := q.GetStackScan(ctx, first.ID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	if got.StackPath != first.StackPath {
		t.Fatalf("expected %s, got %s", first.StackPath, got.StackPath)
	}

	list, err := q.ListProjectStackScans(ctx, "project", 0)
	if err != nil {
		t.Fatalf("list scans: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 scans, got %d", len(list))
	}
	if list[0].ID != second.ID {
		t.Fatalf("expected latest scan first, got %s", list[0].ID)
	}

	limited, err := q.ListProjectStackScans(ctx, "project", 1)
	if err != nil {
		t.Fatalf("list scans limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != second.ID {
		t.Fatalf("expected limited latest scan, got %+v", limited)
	}

	if err := q.removeStackScanRefs(ctx, second); err != nil {
		t.Fatalf("remove refs: %v", err)
	}
	after, err := q.ListProjectStackScans(ctx, "project", 0)
	if err != nil {
		t.Fatalf("list scans after remove: %v", err)
	}
	if len(after) != 1 || after[0].ID != first.ID {
		t.Fatalf("expected remaining scan, got %+v", after)
	}
}
