package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Queue struct {
	client  *redis.Client
	lockTTL time.Duration
}

func New(addr, password string, db int, lockTTL time.Duration) (*Queue, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &Queue{
		client:  client,
		lockTTL: lockTTL,
	}, nil
}

func (q *Queue) Close() error {
	return q.client.Close()
}

// IsRepoLocked checks if a repo scan is in progress.
func (q *Queue) IsRepoLocked(ctx context.Context, repoName string) (bool, error) {
	locked, err := q.client.Exists(ctx, keyLockPrefix+repoName).Result()
	if err != nil {
		return false, err
	}
	return locked > 0, nil
}

func (q *Queue) releaseLock(ctx context.Context, repoName string) error {
	return q.client.Del(ctx, keyLockPrefix+repoName).Err()
}

// Client returns the underlying Redis client for health checks.
func (q *Queue) Client() *redis.Client {
	return q.client
}
