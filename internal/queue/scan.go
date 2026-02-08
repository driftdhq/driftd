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
	ScanStatusRunning   = "running"
	ScanStatusCompleted = "completed"
	ScanStatusFailed    = "failed"
	ScanStatusCanceled  = "canceled"

	scanRenewIntervalMin = 10 * time.Second
)

var ErrScanNotFound = errors.New("scan not found")

type Scan struct {
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

func (q *Queue) StartScan(ctx context.Context, repoName, trigger, commit, actor string, total int) (*Scan, error) {
	if total < 0 {
		total = 0
	}

	scanID := fmt.Sprintf("%s:%d", repoName, time.Now().UnixNano())
	lockKey := keyLockPrefix + repoName

	acquired, err := q.client.SetNX(ctx, lockKey, scanID, q.lockTTL).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire repo lock for %s: %w", repoName, err)
	}
	if !acquired {
		return nil, ErrRepoLocked
	}

	now := time.Now()
	scan := &Scan{
		ID:        scanID,
		RepoName:  repoName,
		Trigger:   trigger,
		Commit:    commit,
		Actor:     actor,
		Status:    ScanStatusRunning,
		CreatedAt: now,
		StartedAt: now,
		Total:     total,
		Queued:    total,
	}

	scanKey := keyScanPrefix + scanID
	pipe := q.client.Pipeline()
	pipe.HSet(ctx, scanKey, map[string]any{
		"id":         scan.ID,
		"repo":       scan.RepoName,
		"trigger":    scan.Trigger,
		"commit":     scan.Commit,
		"actor":      scan.Actor,
		"status":     scan.Status,
		"created_at": scan.CreatedAt.Unix(),
		"started_at": scan.StartedAt.Unix(),
		"ended_at":   0,
		"error":      "",
		"total":      scan.Total,
		"queued":     scan.Queued,
		"running":    scan.Running,
		"completed":  scan.Completed,
		"failed":     scan.Failed,
		"drifted":    scan.Drifted,
		"errored":    scan.Errored,
		"tf_version": "",
		"tg_version": "",
		"stack_tf":   "{}",
		"stack_tg":   "{}",
		"workspace":  "",
		"commit_sha": "",
	})
	pipe.Expire(ctx, scanKey, scanRetention)
	pipe.Set(ctx, keyScanRepo+repoName, scanID, scanRetention)

	if _, err := pipe.Exec(ctx); err != nil {
		q.releaseOwnedLock(ctx, repoName, scanID)
		return nil, fmt.Errorf("failed to create scan: %w", err)
	}

	return scan, nil
}

func (q *Queue) RenewScanLock(ctx context.Context, scanID, repoName string, maxAge, renewEvery time.Duration) {
	start := time.Now()
	if maxAge <= 0 {
		maxAge = 6 * time.Hour
	}
	interval := renewEvery
	if interval <= 0 {
		interval = q.lockTTL / 3
	}

	minInterval := scanRenewIntervalMin
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

		if time.Since(start) > maxAge {
			_ = q.FailScan(context.Background(), scanID, repoName, "scan exceeded maximum duration")
			return
		}

		status, err := q.client.HGet(ctx, keyScanPrefix+scanID, "status").Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return
			}
			continue
		}
		if status != ScanStatusRunning {
			return
		}

		lockValue, err := q.client.Get(ctx, keyLockPrefix+repoName).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return
			}
			continue
		}
		if lockValue != scanID {
			return
		}

		_ = q.client.Expire(ctx, keyLockPrefix+repoName, q.lockTTL).Err()
	}
}

func (q *Queue) GetScan(ctx context.Context, scanID string) (*Scan, error) {
	scanKey := keyScanPrefix + scanID
	values, err := q.client.HGetAll(ctx, scanKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get scan: %w", err)
	}
	if len(values) == 0 {
		return nil, ErrScanNotFound
	}

	return scanFromHash(values)
}

func (q *Queue) SetScanVersions(ctx context.Context, scanID, tfVersion, tgVersion string, stackTF, stackTG map[string]string) error {
	tfJSON, err := json.Marshal(stackTF)
	if err != nil {
		return fmt.Errorf("marshal stack tf versions: %w", err)
	}
	tgJSON, err := json.Marshal(stackTG)
	if err != nil {
		return fmt.Errorf("marshal stack tg versions: %w", err)
	}

	_, err = q.client.HSet(ctx, keyScanPrefix+scanID, map[string]any{
		"tf_version": tfVersion,
		"tg_version": tgVersion,
		"stack_tf":   string(tfJSON),
		"stack_tg":   string(tgJSON),
	}).Result()
	return err
}

func (q *Queue) SetScanTotal(ctx context.Context, scanID string, total int) error {
	_, err := q.client.HSet(ctx, keyScanPrefix+scanID, map[string]any{
		"total":  total,
		"queued": total,
	}).Result()
	return err
}

func (q *Queue) SetScanWorkspace(ctx context.Context, scanID, workspacePath, commitSHA string) error {
	_, err := q.client.HSet(ctx, keyScanPrefix+scanID, map[string]any{
		"workspace":  workspacePath,
		"commit_sha": commitSHA,
	}).Result()
	return err
}

func (q *Queue) FailScan(ctx context.Context, scanID, repoName, errMsg string) error {
	scanKey := keyScanPrefix + scanID
	endedAt := time.Now()

	pipe := q.client.Pipeline()
	pipe.HSet(ctx, scanKey, map[string]any{
		"status":   ScanStatusFailed,
		"ended_at": endedAt.Unix(),
		"error":    errMsg,
	})
	pipe.Del(ctx, keyScanRepo+repoName)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if err := q.releaseOwnedLock(ctx, repoName, scanID); err != nil {
		return err
	}
	_ = q.PublishEvent(ctx, repoName, RepoEvent{
		Type:     "scan_update",
		RepoName: repoName,
		ScanID:   scanID,
		Status:   ScanStatusFailed,
		EndedAt:  &endedAt,
	})
	return nil
}

func (q *Queue) CancelScan(ctx context.Context, scanID, repoName, reason string) error {
	if reason == "" {
		reason = "canceled"
	}
	scanKey := keyScanPrefix + scanID
	endedAt := time.Now()

	pipe := q.client.Pipeline()
	pipe.HSet(ctx, scanKey, map[string]any{
		"status":   ScanStatusCanceled,
		"ended_at": endedAt.Unix(),
		"error":    reason,
	})
	pipe.Del(ctx, keyScanRepo+repoName)
	pipe.Set(ctx, keyScanLast+repoName, scanID, scanRetention)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if err := q.releaseOwnedLock(ctx, repoName, scanID); err != nil {
		return err
	}
	_ = q.PublishEvent(ctx, repoName, RepoEvent{
		Type:     "scan_update",
		RepoName: repoName,
		ScanID:   scanID,
		Status:   ScanStatusCanceled,
		EndedAt:  &endedAt,
	})
	return nil
}

func (q *Queue) GetActiveScan(ctx context.Context, repoName string) (*Scan, error) {
	scanID, err := q.client.Get(ctx, keyScanRepo+repoName).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrScanNotFound
		}
		return nil, fmt.Errorf("failed to get active scan id: %w", err)
	}
	return q.GetScan(ctx, scanID)
}

func (q *Queue) GetLastScan(ctx context.Context, repoName string) (*Scan, error) {
	scanID, err := q.client.Get(ctx, keyScanLast+repoName).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrScanNotFound
		}
		return nil, fmt.Errorf("failed to get last scan id: %w", err)
	}
	return q.GetScan(ctx, scanID)
}

func (q *Queue) AttachStackScanToScan(ctx context.Context, scanID, stackScanID string) error {
	return q.client.SAdd(ctx, keyScanStackScans+scanID, stackScanID).Err()
}

// scanTransitionScript is the core Lua script for atomically updating scan counters
// and auto-finishing the scan when all stack scans are done. It also performs
// compare-and-delete on the lock key to avoid releasing another scan's lock.
//
// KEYS: [1] scan hash key, [2] lock key, [3] scan:repo: key, [4] scan:last: key
// ARGV: [1] scan_id, [2] ended_at, [3..N] pairs of (field, delta) to apply
//
// Returns 1 if the scan was auto-finished, 0 otherwise.
var scanTransitionScript = redis.NewScript(`
local key = KEYS[1]
local lock_key = KEYS[2]
local repo_key = KEYS[3]
local last_key = KEYS[4]
local scan_id = ARGV[1]
local ended_at = ARGV[2]

-- Apply counter deltas (pairs of field, delta starting at ARGV[3])
for i = 3, #ARGV, 2 do
  local field = ARGV[i]
  local delta = tonumber(ARGV[i+1])
  local val = redis.call('HINCRBY', key, field, delta)
  if val < 0 then
    redis.call('HSET', key, field, 0)
  end
end

-- Check if scan should auto-finish
local total = tonumber(redis.call('HGET', key, 'total') or '0')
local comp  = tonumber(redis.call('HGET', key, 'completed') or '0')
local fail  = tonumber(redis.call('HGET', key, 'failed') or '0')

if (total == 0) or (comp + fail >= total) then
  local status = 'completed'
  if fail > 0 then status = 'failed' end
  redis.call('HSET', key, 'status', status, 'ended_at', ended_at)
  -- Compare-and-delete: only release lock if we still own it
  if redis.call('GET', lock_key) == scan_id then
    redis.call('DEL', lock_key)
  end
  redis.call('DEL', repo_key)
  redis.call('SET', last_key, scan_id, 'EX', 604800)
  return 1
end
return 0
`)

func (q *Queue) scanTransitionKeys(scanID, repoName string) []string {
	return []string{
		keyScanPrefix + scanID,
		keyLockPrefix + repoName,
		keyScanRepo + repoName,
		keyScanLast + repoName,
	}
}

func (q *Queue) runScanTransition(ctx context.Context, scanID, repoName string, deltas ...any) error {
	keys := q.scanTransitionKeys(scanID, repoName)
	args := []any{scanID, time.Now().Unix()}
	args = append(args, deltas...)
	return scanTransitionScript.Run(ctx, q.client, keys, args...).Err()
}

func (q *Queue) repoNameForScan(ctx context.Context, scanID string) (string, error) {
	repo, err := q.client.HGet(ctx, keyScanPrefix+scanID, "repo").Result()
	if err != nil {
		return "", fmt.Errorf("failed to get repo for scan %s: %w", scanID, err)
	}
	return repo, nil
}

func (q *Queue) markScanStackScanRunning(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "running", 1, "queued", -1)
}

func (q *Queue) markScanStackScanRetry(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "running", -1, "queued", 1)
}

func (q *Queue) markScanStackScanCompleted(ctx context.Context, scanID string, drifted bool) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	deltas := []any{"running", -1, "completed", 1}
	if drifted {
		deltas = append(deltas, "drifted", 1)
	}
	return q.runScanTransition(ctx, scanID, repoName, deltas...)
}

func (q *Queue) markScanStackScanFailed(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "running", -1, "failed", 1, "errored", 1)
}

func (q *Queue) MarkScanEnqueueFailed(ctx context.Context, scanID string) error {
	repoName, err := q.repoNameForScan(ctx, scanID)
	if err != nil {
		return err
	}
	return q.runScanTransition(ctx, scanID, repoName, "queued", -1, "failed", 1, "errored", 1)
}

func scanFromHash(values map[string]string) (*Scan, error) {
	var stackTF map[string]string
	var stackTG map[string]string
	if raw := values["stack_tf"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &stackTF)
	}
	if raw := values["stack_tg"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &stackTG)
	}

	scan := &Scan{
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

	scan.CreatedAt = time.Unix(toInt64(values["created_at"]), 0)
	scan.StartedAt = time.Unix(toInt64(values["started_at"]), 0)
	scan.EndedAt = time.Unix(toInt64(values["ended_at"]), 0)

	return scan, nil
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
