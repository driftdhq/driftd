package queue

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrCloneLockNotOwned = errors.New("clone lock not owned by caller")

var releaseCloneLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

var renewCloneLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

func (q *Queue) AcquireCloneLock(ctx context.Context, urlHash, owner string, ttl time.Duration) (bool, error) {
	return q.client.SetNX(ctx, keyCloneLockPrefix+urlHash, owner, ttl).Result()
}

func (q *Queue) RenewCloneLock(ctx context.Context, urlHash, owner string, ttl time.Duration) error {
	renewed, err := renewCloneLockScript.Run(ctx, q.client, []string{keyCloneLockPrefix + urlHash}, owner, ttl.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if renewed == 0 {
		return ErrCloneLockNotOwned
	}
	return nil
}

func (q *Queue) ReleaseCloneLock(ctx context.Context, urlHash, owner string) error {
	released, err := releaseCloneLockScript.Run(ctx, q.client, []string{keyCloneLockPrefix + urlHash}, owner).Int64()
	if err != nil {
		return err
	}
	if released == 0 {
		return ErrCloneLockNotOwned
	}
	return nil
}
