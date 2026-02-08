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
		ID:        "scan-1",
		RepoName:  "repo",
		StackPath: "envs/dev",
		Status:    StatusCompleted,
		CreatedAt: now.Add(-time.Minute),
	}
	second := &StackScan{
		ID:        "scan-2",
		RepoName:  "repo",
		StackPath: "envs/prod",
		Status:    StatusCompleted,
		CreatedAt: now,
	}

	for _, scan := range []*StackScan{first, second} {
		if err := q.saveStackScan(ctx, scan); err != nil {
			t.Fatalf("save scan: %v", err)
		}
		if err := q.client.SAdd(ctx, keyRepoStackScans+scan.RepoName, scan.ID).Err(); err != nil {
			t.Fatalf("sadd: %v", err)
		}
		if err := q.client.ZAdd(ctx, keyRepoStackScansOrdered+scan.RepoName, redis.Z{
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

	list, err := q.ListRepoStackScans(ctx, "repo", 0)
	if err != nil {
		t.Fatalf("list scans: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 scans, got %d", len(list))
	}
	if list[0].ID != second.ID {
		t.Fatalf("expected latest scan first, got %s", list[0].ID)
	}

	limited, err := q.ListRepoStackScans(ctx, "repo", 1)
	if err != nil {
		t.Fatalf("list scans limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != second.ID {
		t.Fatalf("expected limited latest scan, got %+v", limited)
	}

	if err := q.removeStackScanRefs(ctx, second); err != nil {
		t.Fatalf("remove refs: %v", err)
	}
	after, err := q.ListRepoStackScans(ctx, "repo", 0)
	if err != nil {
		t.Fatalf("list scans after remove: %v", err)
	}
	if len(after) != 1 || after[0].ID != first.ID {
		t.Fatalf("expected remaining scan, got %+v", after)
	}
}
