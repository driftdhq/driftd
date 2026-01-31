package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"

	keyQueue      = "driftd:queue:scans"
	keyJobPrefix  = "driftd:job:"
	keyLockPrefix = "driftd:lock:repo:"
	keyRepoJobs   = "driftd:jobs:repo:"

	jobRetention = 7 * 24 * time.Hour // 7 days
)

var (
	ErrRepoLocked  = errors.New("repository scan already in progress")
	ErrJobNotFound = errors.New("job not found")
)

type Job struct {
	ID          string    `json:"id"`
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

// Enqueue adds a job to the queue. Returns ErrRepoLocked if repo is already being scanned.
func (q *Queue) Enqueue(ctx context.Context, job *Job) error {
	// Check if repo is locked
	lockKey := keyLockPrefix + job.RepoName
	locked, err := q.client.Exists(ctx, lockKey).Result()
	if err != nil {
		return fmt.Errorf("failed to check lock: %w", err)
	}
	if locked > 0 {
		return ErrRepoLocked
	}

	// Set job defaults
	job.Status = StatusPending
	job.CreatedAt = time.Now()
	if job.ID == "" {
		job.ID = fmt.Sprintf("%s:%s:%d", job.RepoName, job.StackPath, job.CreatedAt.UnixNano())
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
		// Block waiting for job
		result, err := q.client.BRPop(ctx, 0, keyQueue).Result()
		if err != nil {
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

		// Try to acquire repo lock
		lockKey := keyLockPrefix + job.RepoName
		acquired, err := q.client.SetNX(ctx, lockKey, workerID, q.lockTTL).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to acquire lock: %w", err)
		}
		if !acquired {
			// Repo is locked, re-queue the job and try another
			q.client.LPush(ctx, keyQueue, jobID)
			continue
		}

		// Update job status
		job.Status = StatusRunning
		job.StartedAt = time.Now()
		job.WorkerID = workerID
		if err := q.updateJob(ctx, job); err != nil {
			q.releaseLock(ctx, job.RepoName)
			return nil, err
		}

		return job, nil
	}
}

// Complete marks a job as completed and releases the repo lock.
func (q *Queue) Complete(ctx context.Context, job *Job) error {
	job.Status = StatusCompleted
	job.CompletedAt = time.Now()
	if err := q.updateJob(ctx, job); err != nil {
		return err
	}
	return q.releaseLock(ctx, job.RepoName)
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
		q.releaseLock(ctx, job.RepoName)
		return q.client.LPush(ctx, keyQueue, job.ID).Err()
	}

	job.Status = StatusFailed
	job.CompletedAt = time.Now()
	if err := q.updateJob(ctx, job); err != nil {
		return err
	}
	return q.releaseLock(ctx, job.RepoName)
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

	var jobs []*Job
	for _, id := range jobIDs {
		job, err := q.GetJob(ctx, id)
		if err != nil {
			continue // Job expired
		}
		jobs = append(jobs, job)
		if limit > 0 && len(jobs) >= limit {
			break
		}
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
