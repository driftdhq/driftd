package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCloneLockAcquireReleaseOwnership(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()
	urlHash := "abc123"

	acquired, err := q.AcquireCloneLock(ctx, urlHash, "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired {
		t.Fatalf("expected first acquire to succeed")
	}

	acquired, err = q.AcquireCloneLock(ctx, urlHash, "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if acquired {
		t.Fatalf("expected second acquire to fail while lock held")
	}

	if err := q.ReleaseCloneLock(ctx, urlHash, "owner-b"); !errors.Is(err, ErrCloneLockNotOwned) {
		t.Fatalf("expected ErrCloneLockNotOwned, got %v", err)
	}
	if exists, err := q.client.Exists(ctx, keyCloneLockPrefix+urlHash).Result(); err != nil || exists != 1 {
		t.Fatalf("expected lock to remain after non-owner release, exists=%d err=%v", exists, err)
	}

	if err := q.ReleaseCloneLock(ctx, urlHash, "owner-a"); err != nil {
		t.Fatalf("release owner: %v", err)
	}
	if exists, err := q.client.Exists(ctx, keyCloneLockPrefix+urlHash).Result(); err != nil || exists != 0 {
		t.Fatalf("expected lock deleted after owner release, exists=%d err=%v", exists, err)
	}
}

func TestCloneLockRenewOwnerOnly(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()
	urlHash := "renew456"

	acquired, err := q.AcquireCloneLock(ctx, urlHash, "owner-a", time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired {
		t.Fatalf("expected acquire to succeed")
	}

	if err := q.RenewCloneLock(ctx, urlHash, "owner-b", 5*time.Second); !errors.Is(err, ErrCloneLockNotOwned) {
		t.Fatalf("expected ErrCloneLockNotOwned on renew, got %v", err)
	}

	if err := q.RenewCloneLock(ctx, urlHash, "owner-a", 5*time.Second); err != nil {
		t.Fatalf("renew owner: %v", err)
	}

	ttl, err := q.client.PTTL(ctx, keyCloneLockPrefix+urlHash).Result()
	if err != nil {
		t.Fatalf("pttl: %v", err)
	}
	if ttl < 4*time.Second {
		t.Fatalf("expected renewed ttl around 5s, got %s", ttl)
	}
}
