package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"

	keyQueue      = "driftd:queue:workitems"
	keyJobPrefix  = "driftd:job:"
	keyLockPrefix = "driftd:lock:repo:"
	keyRepoJobs   = "driftd:jobs:repo:"
	keyTaskPrefix = "driftd:task:"
	keyTaskRepo   = "driftd:task:repo:"
	keyTaskJobs   = "driftd:task:jobs:"
	keyTaskLast   = "driftd:task:last:"

	jobRetention = 7 * 24 * time.Hour // 7 days
)

var (
	ErrRepoLocked  = errors.New("repository scan already in progress")
	ErrJobNotFound = errors.New("job not found")
)

type Job struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	RepoName    string    `json:"repo_name"`
	RepoURL     string    `json:"repo_url"`
	StackPath   string    `json:"stack_path"`
	Status      string    `json:"status"`
	Retries     int       `json:"retries"`
	MaxRetries  int       `json:"max_retries"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	WorkerID    string    `json:"worker_id,omitempty"`
	Error       string    `json:"error,omitempty"`

	// Optional metadata from trigger
	Trigger string `json:"trigger,omitempty"` // "scheduled", "manual", "post-apply"
	Commit  string `json:"commit,omitempty"`
	Actor   string `json:"actor,omitempty"`
}

func (q *Queue) CancelJob(ctx context.Context, job *Job, reason string) error {
	job.Status = StatusCanceled
	job.CompletedAt = time.Now()
	job.Error = reason
	if err := q.updateJob(ctx, job); err != nil {
		return err
	}
	return q.client.SRem(ctx, keyRepoJobs+job.RepoName, job.ID).Err()
}

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

// Enqueue adds a job to the queue.
func (q *Queue) Enqueue(ctx context.Context, job *Job) error {
	// Set job defaults
	job.Status = StatusPending
	job.CreatedAt = time.Now()
	if job.ID == "" {
		job.ID = fmt.Sprintf("%s:%s:%d:%d", job.RepoName, job.StackPath, job.CreatedAt.UnixNano(), rand.Int31())
	}

	// Store job
	jobKey := keyJobPrefix + job.ID
	jobData, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	pipe := q.client.Pipeline()
	pipe.Set(ctx, jobKey, jobData, jobRetention)
	pipe.SAdd(ctx, keyRepoJobs+job.RepoName, job.ID)
	if job.TaskID != "" {
		pipe.SAdd(ctx, keyTaskJobs+job.TaskID, job.ID)
	}
	pipe.LPush(ctx, keyQueue, job.ID)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to enqueue job: %w", err)
	}

	return nil
}

// Dequeue blocks until a job is available, then returns it.
// The job status is updated to "running" and the repo lock is acquired.
func (q *Queue) Dequeue(ctx context.Context, workerID string) (*Job, error) {
	for {
		// Block waiting for job (1 second timeout to allow checking for orphaned jobs)
		result, err := q.client.BRPop(ctx, time.Second, keyQueue).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				job, findErr := q.findPendingJob(ctx)
				if findErr != nil {
					return nil, findErr
				}
				if job == nil {
					continue
				}
				if err := q.markJobRunning(ctx, job, workerID); err != nil {
					return nil, err
				}
				return job, nil
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to dequeue: %w", err)
		}

		jobID := result[1]
		job, err := q.GetJob(ctx, jobID)
		if err != nil {
			// Job expired or deleted, try next
			continue
		}

		if err := q.markJobRunning(ctx, job, workerID); err != nil {
			return nil, err
		}

		return job, nil
	}
}

func (q *Queue) markJobRunning(ctx context.Context, job *Job, workerID string) error {
	job.Status = StatusRunning
	job.StartedAt = time.Now()
	job.WorkerID = workerID
	if err := q.updateJob(ctx, job); err != nil {
		return err
	}
	if job.TaskID != "" {
		if err := q.markTaskJobRunning(ctx, job.TaskID); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) findPendingJob(ctx context.Context) (*Job, error) {
	var cursor uint64
	for {
		keys, next, err := q.client.Scan(ctx, cursor, keyJobPrefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			data, err := q.client.Get(ctx, key).Result()
			if err != nil {
				if errors.Is(err, redis.Nil) {
					continue
				}
				return nil, err
			}
			var job Job
			if err := json.Unmarshal([]byte(data), &job); err != nil {
				continue
			}
			if job.Status == StatusPending {
				return &job, nil
			}
		}
		if next == 0 {
			return nil, nil
		}
		cursor = next
	}
}

// Complete marks a job as completed and releases the repo lock.
func (q *Queue) Complete(ctx context.Context, job *Job, drifted bool) error {
	job.Status = StatusCompleted
	job.CompletedAt = time.Now()
	if err := q.updateJob(ctx, job); err != nil {
		return err
	}
	if err := q.client.SRem(ctx, keyRepoJobs+job.RepoName, job.ID).Err(); err != nil {
		return err
	}
	if job.TaskID != "" {
		return q.markTaskJobCompleted(ctx, job.TaskID, drifted)
	}
	return nil
}

// Fail marks a job as failed. If retries remain, re-queues it.
func (q *Queue) Fail(ctx context.Context, job *Job, errMsg string) error {
	job.Error = errMsg
	job.Retries++

	if job.Retries <= job.MaxRetries {
		// Re-queue for retry
		job.Status = StatusPending
		job.StartedAt = time.Time{}
		job.WorkerID = ""
		if err := q.updateJob(ctx, job); err != nil {
			return err
		}
		if job.TaskID != "" {
			if err := q.markTaskJobRetry(ctx, job.TaskID); err != nil {
				return err
			}
		}
		return q.client.LPush(ctx, keyQueue, job.ID).Err()
	}

	job.Status = StatusFailed
	job.CompletedAt = time.Now()
	if err := q.updateJob(ctx, job); err != nil {
		return err
	}
	if err := q.client.SRem(ctx, keyRepoJobs+job.RepoName, job.ID).Err(); err != nil {
		return err
	}
	if job.TaskID != "" {
		return q.markTaskJobFailed(ctx, job.TaskID)
	}
	return nil
}

// GetJob retrieves a job by ID.
func (q *Queue) GetJob(ctx context.Context, jobID string) (*Job, error) {
	jobKey := keyJobPrefix + jobID
	data, err := q.client.Get(ctx, jobKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	var job Job
	if err := json.Unmarshal([]byte(data), &job); err != nil {
		return nil, fmt.Errorf("failed to unmarshal job: %w", err)
	}
	return &job, nil
}

// ListRepoJobs returns recent jobs for a repo.
func (q *Queue) ListRepoJobs(ctx context.Context, repoName string, limit int) ([]*Job, error) {
	jobIDs, err := q.client.SMembers(ctx, keyRepoJobs+repoName).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list job IDs: %w", err)
	}

	if len(jobIDs) == 0 {
		return nil, nil
	}
	if limit > 0 && len(jobIDs) > limit {
		jobIDs = jobIDs[:limit]
	}

	pipe := q.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(jobIDs))
	for i, id := range jobIDs {
		cmds[i] = pipe.Get(ctx, keyJobPrefix+id)
	}
	_, err = pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to fetch jobs: %w", err)
	}

	var jobs []*Job
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue // Job expired
		}
		var job Job
		if err := json.Unmarshal([]byte(data), &job); err != nil {
			continue
		}
		jobs = append(jobs, &job)
	}

	return jobs, nil
}

// IsRepoLocked checks if a repo scan is in progress.
func (q *Queue) IsRepoLocked(ctx context.Context, repoName string) (bool, error) {
	locked, err := q.client.Exists(ctx, keyLockPrefix+repoName).Result()
	if err != nil {
		return false, err
	}
	return locked > 0, nil
}

func (q *Queue) updateJob(ctx context.Context, job *Job) error {
	jobKey := keyJobPrefix + job.ID
	jobData, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}
	return q.client.Set(ctx, jobKey, jobData, jobRetention).Err()
}

func (q *Queue) releaseLock(ctx context.Context, repoName string) error {
	return q.client.Del(ctx, keyLockPrefix+repoName).Err()
}

// Client returns the underlying Redis client for health checks.
func (q *Queue) Client() *redis.Client {
	return q.client
}
