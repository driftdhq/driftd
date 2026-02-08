package queue

import (
	"context"
	"time"
)

func (q *Queue) RunningStackScanCount(ctx context.Context) (int, error) {
	count, err := q.client.ZCard(ctx, keyRunningStackScans).Result()
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func (q *Queue) OldestRunningStackScanAge(ctx context.Context) (time.Duration, error) {
	res, err := q.client.ZRangeWithScores(ctx, keyRunningStackScans, 0, 0).Result()
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	startedAt := time.Unix(int64(res[0].Score), 0)
	if startedAt.After(time.Now()) {
		return 0, nil
	}
	return time.Since(startedAt), nil
}

func (q *Queue) RunningScanCount(ctx context.Context) (int, error) {
	count, err := q.client.ZCard(ctx, keyRunningScans).Result()
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func (q *Queue) OldestRunningScanAge(ctx context.Context) (time.Duration, error) {
	res, err := q.client.ZRangeWithScores(ctx, keyRunningScans, 0, 0).Result()
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	startedAt := time.Unix(int64(res[0].Score), 0)
	if startedAt.After(time.Now()) {
		return 0, nil
	}
	return time.Since(startedAt), nil
}
