package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	TaskStatusRunning   = "running"
	TaskStatusCompleted = "completed"
	TaskStatusFailed    = "failed"
	TaskStatusCanceled  = "canceled"

	taskRenewIntervalMin = 10 * time.Second
)

var ErrTaskNotFound = errors.New("task not found")

type Task struct {
	ID        string    `json:"id"`
	RepoName  string    `json:"repo_name"`
	Trigger   string    `json:"trigger,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Error     string    `json:"error,omitempty"`

	TerraformVersion  string            `json:"terraform_version,omitempty"`
	TerragruntVersion string            `json:"terragrunt_version,omitempty"`
	StackTFVersions   map[string]string `json:"stack_tf_versions,omitempty"`
	StackTGVersions   map[string]string `json:"stack_tg_versions,omitempty"`
	WorkspacePath     string            `json:"workspace_path,omitempty"`
	CommitSHA         string            `json:"commit_sha,omitempty"`

	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Drifted   int `json:"drifted"`
	Errored   int `json:"errored"`
}

func (q *Queue) StartTask(ctx context.Context, repoName, trigger, commit, actor string, total int) (*Task, error) {
	if total < 0 {
		total = 0
	}

	taskID := fmt.Sprintf("%s:%d", repoName, time.Now().UnixNano())
	lockKey := keyLockPrefix + repoName

	acquired, err := q.client.SetNX(ctx, lockKey, taskID, q.lockTTL).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire repo lock: %w", err)
	}
	if !acquired {
		return nil, ErrRepoLocked
	}

	now := time.Now()
	task := &Task{
		ID:        taskID,
		RepoName:  repoName,
		Trigger:   trigger,
		Commit:    commit,
		Actor:     actor,
		Status:    TaskStatusRunning,
		CreatedAt: now,
		StartedAt: now,
		Total:     total,
		Queued:    total,
	}

	taskKey := keyTaskPrefix + taskID
	pipe := q.client.Pipeline()
	pipe.HSet(ctx, taskKey, map[string]any{
		"id":         task.ID,
		"repo":       task.RepoName,
		"trigger":    task.Trigger,
		"commit":     task.Commit,
		"actor":      task.Actor,
		"status":     task.Status,
		"created_at": task.CreatedAt.Unix(),
		"started_at": task.StartedAt.Unix(),
		"ended_at":   0,
		"error":      "",
		"total":      task.Total,
		"queued":     task.Queued,
		"running":    task.Running,
		"completed":  task.Completed,
		"failed":     task.Failed,
		"drifted":    task.Drifted,
		"errored":    task.Errored,
		"tf_version": "",
		"tg_version": "",
		"stack_tf":   "{}",
		"stack_tg":   "{}",
		"workspace":  "",
		"commit_sha": "",
	})
	pipe.Expire(ctx, taskKey, jobRetention)
	pipe.Set(ctx, keyTaskRepo+repoName, taskID, jobRetention)

	if _, err := pipe.Exec(ctx); err != nil {
		q.releaseLock(ctx, repoName)
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	return task, nil
}

func (q *Queue) RenewTaskLock(ctx context.Context, taskID, repoName string, maxAge, renewEvery time.Duration) {
	start := time.Now()
	interval := renewEvery
	if interval <= 0 {
		interval = q.lockTTL / 3
	}

	minInterval := taskRenewIntervalMin
	if interval < minInterval {
		interval = minInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if maxAge > 0 && time.Since(start) > maxAge {
			_ = q.FailTask(context.Background(), taskID, repoName, "task exceeded maximum duration")
			return
		}

		status, err := q.client.HGet(ctx, keyTaskPrefix+taskID, "status").Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return
			}
			continue
		}
		if status != TaskStatusRunning {
			return
		}

		lockValue, err := q.client.Get(ctx, keyLockPrefix+repoName).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return
			}
			continue
		}
		if lockValue != taskID {
			return
		}

		_ = q.client.Expire(ctx, keyLockPrefix+repoName, q.lockTTL).Err()
	}
}

func (q *Queue) GetTask(ctx context.Context, taskID string) (*Task, error) {
	taskKey := keyTaskPrefix + taskID
	values, err := q.client.HGetAll(ctx, taskKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if len(values) == 0 {
		return nil, ErrTaskNotFound
	}

	return taskFromHash(values)
}

func (q *Queue) SetTaskVersions(ctx context.Context, taskID, tfVersion, tgVersion string, stackTF, stackTG map[string]string) error {
	tfJSON, err := json.Marshal(stackTF)
	if err != nil {
		return fmt.Errorf("marshal stack tf versions: %w", err)
	}
	tgJSON, err := json.Marshal(stackTG)
	if err != nil {
		return fmt.Errorf("marshal stack tg versions: %w", err)
	}

	_, err = q.client.HSet(ctx, keyTaskPrefix+taskID, map[string]any{
		"tf_version": tfVersion,
		"tg_version": tgVersion,
		"stack_tf":   string(tfJSON),
		"stack_tg":   string(tgJSON),
	}).Result()
	return err
}

func (q *Queue) SetTaskTotal(ctx context.Context, taskID string, total int) error {
	_, err := q.client.HSet(ctx, keyTaskPrefix+taskID, map[string]any{
		"total":  total,
		"queued": total,
	}).Result()
	return err
}

func (q *Queue) SetTaskWorkspace(ctx context.Context, taskID, workspacePath, commitSHA string) error {
	_, err := q.client.HSet(ctx, keyTaskPrefix+taskID, map[string]any{
		"workspace":  workspacePath,
		"commit_sha": commitSHA,
	}).Result()
	return err
}

func (q *Queue) FailTask(ctx context.Context, taskID, repoName, errMsg string) error {
	taskKey := keyTaskPrefix + taskID

	pipe := q.client.Pipeline()
	pipe.HSet(ctx, taskKey, map[string]any{
		"status":   TaskStatusFailed,
		"ended_at": time.Now().Unix(),
		"error":    errMsg,
	})
	pipe.Del(ctx, keyLockPrefix+repoName)
	pipe.Del(ctx, keyTaskRepo+repoName)
	_, err := pipe.Exec(ctx)
	return err
}

func (q *Queue) CancelTask(ctx context.Context, taskID, repoName, reason string) error {
	if reason == "" {
		reason = "canceled"
	}
	taskKey := keyTaskPrefix + taskID

	pipe := q.client.Pipeline()
	pipe.HSet(ctx, taskKey, map[string]any{
		"status":   TaskStatusCanceled,
		"ended_at": time.Now().Unix(),
		"error":    reason,
	})
	pipe.Del(ctx, keyLockPrefix+repoName)
	pipe.Del(ctx, keyTaskRepo+repoName)
	pipe.Set(ctx, keyTaskLast+repoName, taskID, jobRetention)
	_, err := pipe.Exec(ctx)
	return err
}
func (q *Queue) GetActiveTask(ctx context.Context, repoName string) (*Task, error) {
	taskID, err := q.client.Get(ctx, keyTaskRepo+repoName).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("failed to get active task id: %w", err)
	}
	return q.GetTask(ctx, taskID)
}

func (q *Queue) GetLastTask(ctx context.Context, repoName string) (*Task, error) {
	taskID, err := q.client.Get(ctx, keyTaskLast+repoName).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("failed to get last task id: %w", err)
	}
	return q.GetTask(ctx, taskID)
}

func (q *Queue) AttachJobToTask(ctx context.Context, taskID, jobID string) error {
	return q.client.SAdd(ctx, keyTaskJobs+taskID, jobID).Err()
}

func (q *Queue) markTaskJobRunning(ctx context.Context, taskID string) error {
	pipe := q.client.Pipeline()
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "running", 1)
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "queued", -1)
	_, err := pipe.Exec(ctx)
	return err
}

func (q *Queue) markTaskJobRetry(ctx context.Context, taskID string) error {
	pipe := q.client.Pipeline()
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "running", -1)
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "queued", 1)
	_, err := pipe.Exec(ctx)
	return err
}

func (q *Queue) markTaskJobCompleted(ctx context.Context, taskID string, drifted bool) error {
	pipe := q.client.Pipeline()
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "running", -1)
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "completed", 1)
	if drifted {
		pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "drifted", 1)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return q.maybeFinishTask(ctx, taskID)
}

func (q *Queue) markTaskJobFailed(ctx context.Context, taskID string) error {
	pipe := q.client.Pipeline()
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "running", -1)
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "failed", 1)
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "errored", 1)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return q.maybeFinishTask(ctx, taskID)
}

func (q *Queue) MarkTaskEnqueueFailed(ctx context.Context, taskID string) error {
	pipe := q.client.Pipeline()
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "queued", -1)
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "failed", 1)
	pipe.HIncrBy(ctx, keyTaskPrefix+taskID, "errored", 1)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return q.maybeFinishTask(ctx, taskID)
}

func (q *Queue) maybeFinishTask(ctx context.Context, taskID string) error {
	taskKey := keyTaskPrefix + taskID
	values, err := q.client.HMGet(ctx, taskKey, "repo", "total", "completed", "failed").Result()
	if err != nil {
		return err
	}
	if len(values) != 4 {
		return nil
	}

	repoName, _ := values[0].(string)
	total := toInt(values[1])
	completed := toInt(values[2])
	failed := toInt(values[3])

	if total == 0 {
		return q.finishTask(ctx, taskKey, repoName, 0)
	}
	if completed+failed < total {
		return nil
	}
	return q.finishTask(ctx, taskKey, repoName, taskID, failed)
}

func (q *Queue) finishTask(ctx context.Context, taskKey, repoName, taskID string, failed int) error {
	status := TaskStatusCompleted
	if failed > 0 {
		status = TaskStatusFailed
	}

	pipe := q.client.Pipeline()
	pipe.HSet(ctx, taskKey, map[string]any{
		"status":   status,
		"ended_at": time.Now().Unix(),
	})
	pipe.Del(ctx, keyLockPrefix+repoName)
	pipe.Del(ctx, keyTaskRepo+repoName)
	pipe.Set(ctx, keyTaskLast+repoName, taskID, jobRetention)
	_, err := pipe.Exec(ctx)
	return err
}

func taskFromHash(values map[string]string) (*Task, error) {
	var stackTF map[string]string
	var stackTG map[string]string
	if raw := values["stack_tf"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &stackTF)
	}
	if raw := values["stack_tg"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &stackTG)
	}

	task := &Task{
		ID:                values["id"],
		RepoName:          values["repo"],
		Trigger:           values["trigger"],
		Commit:            values["commit"],
		Actor:             values["actor"],
		Status:            values["status"],
		Error:             values["error"],
		TerraformVersion:  values["tf_version"],
		TerragruntVersion: values["tg_version"],
		StackTFVersions:   stackTF,
		StackTGVersions:   stackTG,
		WorkspacePath:     values["workspace"],
		CommitSHA:         values["commit_sha"],
		Total:             toInt(values["total"]),
		Queued:            toInt(values["queued"]),
		Running:           toInt(values["running"]),
		Completed:         toInt(values["completed"]),
		Failed:            toInt(values["failed"]),
		Drifted:           toInt(values["drifted"]),
		Errored:           toInt(values["errored"]),
	}

	task.CreatedAt = time.Unix(toInt64(values["created_at"]), 0)
	task.StartedAt = time.Unix(toInt64(values["started_at"]), 0)
	task.EndedAt = time.Unix(toInt64(values["ended_at"]), 0)

	return task, nil
}

func toInt(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		i, _ := strconv.Atoi(v)
		return i
	case int64:
		return int(v)
	default:
		return 0
	}
}

func toInt64(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	case int64:
		return v
	default:
		return 0
	}
}
