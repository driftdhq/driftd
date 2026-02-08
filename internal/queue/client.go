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

func (q *Queue) QueueDepth(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, keyQueue).Result()
}

// IsRepoLocked checks if a repo scan is in progress.
func (q *Queue) IsRepoLocked(ctx context.Context, repoName string) (bool, error) {
	locked, err := q.client.Exists(ctx, keyLockPrefix+repoName).Result()
	if err != nil {
		return false, err
	}
	return locked > 0, nil
}

// releaseOwnedLock deletes the lock only if it is still owned by the given scanID.
// This prevents accidentally releasing a lock that was re-acquired by a different scan.
func (q *Queue) releaseOwnedLock(ctx context.Context, repoName, scanID string) error {
	script := redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)
	return script.Run(ctx, q.client, []string{keyLockPrefix + repoName}, scanID).Err()
}

// Client returns the underlying Redis client for health checks.
func (q *Queue) Client() *redis.Client {
	return q.client
}
