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

var renewLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// cancelAndAcquireScript atomically cancels an existing scan and acquires
// the lock for a new scan. This prevents the race where another process
// grabs the lock between cancel and start.
//
// KEYS: [1] lock key, [2] old scan hash, [3] scan:project key, [4] scan:last key, [5] running scans zset
// ARGV: [1] old scan ID, [2] new scan ID, [3] lock TTL ms, [4] ended_at, [5] cancel reason, [6] scan retention seconds
//
// Returns 1 if successful, 0 if lock not owned by old scan.
var cancelAndAcquireScript = redis.NewScript(`
local lock_key = KEYS[1]
local old_scan_key = KEYS[2]
local repo_key = KEYS[3]
local last_key = KEYS[4]
local running_key = KEYS[5]
local old_scan_id = ARGV[1]
local new_scan_id = ARGV[2]
local lock_ttl_ms = ARGV[3]
local ended_at = ARGV[4]
local reason = ARGV[5]
local retention = tonumber(ARGV[6])

local current = redis.call('GET', lock_key)
if current ~= old_scan_id then
  return 0
end

-- Cancel old scan
redis.call('HSET', old_scan_key, 'status', 'canceled', 'ended_at', ended_at, 'error', reason)
redis.call('ZREM', running_key, old_scan_id)
redis.call('SET', last_key, old_scan_id, 'EX', retention)

-- Acquire lock for new scan
redis.call('SET', lock_key, new_scan_id, 'PX', lock_ttl_ms)
redis.call('SET', repo_key, new_scan_id, 'EX', retention)

return 1
`)

type Scan struct {
	ID          string    `json:"id"`
	ProjectName string    `json:"project_name"`
	Trigger     string    `json:"trigger,omitempty"`
	Commit      string    `json:"commit,omitempty"`
	Actor       string    `json:"actor,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	Error       string    `json:"error,omitempty"`

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

func (q *Queue) StartScan(ctx context.Context, projectName, trigger, commit, actor string, total int) (*Scan, error) {
	if total < 0 {
		total = 0
	}

	scanID := fmt.Sprintf("%s:%d", projectName, time.Now().UnixNano())
	lockKey := keyLockPrefix + projectName

	acquired, err := q.client.SetNX(ctx, lockKey, scanID, q.lockTTL).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire project lock for %s: %w", projectName, err)
	}
	if !acquired {
		return nil, ErrProjectLocked
	}

	now := time.Now()
	scan := &Scan{
		ID:          scanID,
		ProjectName: projectName,
		Trigger:     trigger,
		Commit:      commit,
		Actor:       actor,
		Status:      ScanStatusRunning,
		CreatedAt:   now,
		StartedAt:   now,
		Total:       total,
		Queued:      total,
	}

	scanKey := keyScanPrefix + scanID
	pipe := q.client.Pipeline()
	pipe.HSet(ctx, scanKey, map[string]any{
		"id":         scan.ID,
		"project":    scan.ProjectName,
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
	pipe.Set(ctx, keyScanRepo+projectName, scanID, scanRetention)
	pipe.ZAdd(ctx, keyRunningScans, redis.Z{
		Score:  float64(scan.StartedAt.Unix()),
		Member: scan.ID,
	})

	if _, err := pipe.Exec(ctx); err != nil {
		q.releaseOwnedLock(ctx, projectName, scanID)
		return nil, fmt.Errorf("failed to create scan: %w", err)
	}

	return scan, nil
}

// CancelAndStartScan atomically cancels an existing scan and starts a new one.
// This prevents the race condition where another caller could acquire the lock
// between a separate CancelScan + StartScan.
func (q *Queue) CancelAndStartScan(ctx context.Context, oldScanID, projectName, cancelReason, trigger, commit, actor string, total int) (*Scan, error) {
	if total < 0 {
		total = 0
	}

	newScanID := fmt.Sprintf("%s:%d", projectName, time.Now().UnixNano())
	endedAt := time.Now().Unix()
	retentionSeconds := int(scanRetention.Seconds())

	result, err := cancelAndAcquireScript.Run(ctx, q.client,
		[]string{
			keyLockPrefix + projectName,
			keyScanPrefix + oldScanID,
			keyScanRepo + projectName,
			keyScanLast + projectName,
			keyRunningScans,
		},
		oldScanID,
		newScanID,
		q.lockTTL.Milliseconds(),
		endedAt,
		cancelReason,
		retentionSeconds,
	).Int64()
	if err != nil {
		return nil, fmt.Errorf("cancel-and-acquire failed: %w", err)
	}
	if result == 0 {
		return nil, ErrProjectLocked
	}

	// Publish cancel event for old scan
	endedAtTime := time.Unix(endedAt, 0)
	_ = q.PublishScanEvent(ctx, projectName, ScanEvent{
		ProjectName: projectName,
		ScanID:      oldScanID,
		Status:      ScanStatusCanceled,
		EndedAt:     &endedAtTime,
	})

	// Create the new scan hash
	now := time.Now()
	scan := &Scan{
		ID:          newScanID,
		ProjectName: projectName,
		Trigger:     trigger,
		Commit:      commit,
		Actor:       actor,
		Status:      ScanStatusRunning,
		CreatedAt:   now,
		StartedAt:   now,
		Total:       total,
		Queued:      total,
	}

	scanKey := keyScanPrefix + newScanID
	pipe := q.client.Pipeline()
	pipe.HSet(ctx, scanKey, map[string]any{
		"id":         scan.ID,
		"project":    scan.ProjectName,
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
	pipe.ZAdd(ctx, keyRunningScans, redis.Z{
		Score:  float64(scan.StartedAt.Unix()),
		Member: scan.ID,
	})

	if _, err := pipe.Exec(ctx); err != nil {
		q.releaseOwnedLock(ctx, projectName, newScanID)
		return nil, fmt.Errorf("failed to create scan after cancel: %w", err)
	}

	return scan, nil
}

func (q *Queue) RenewScanLock(ctx context.Context, scanID, projectName string, maxAge, renewEvery time.Duration) {
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
			_ = q.FailScan(context.Background(), scanID, projectName, "scan exceeded maximum duration")
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

		renewed, err := renewLockScript.Run(ctx, q.client,
			[]string{keyLockPrefix + projectName},
			scanID, q.lockTTL.Milliseconds(),
		).Int64()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return
			}
			continue
		}
		if renewed == 0 {
			return
		}
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

func (q *Queue) FailScan(ctx context.Context, scanID, projectName, errMsg string) error {
	scanKey := keyScanPrefix + scanID
	endedAt := time.Now()

	pipe := q.client.Pipeline()
	pipe.HSet(ctx, scanKey, map[string]any{
		"status":   ScanStatusFailed,
		"ended_at": endedAt.Unix(),
		"error":    errMsg,
	})
	pipe.Del(ctx, keyScanRepo+projectName)
	pipe.ZRem(ctx, keyRunningScans, scanID)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if err := q.releaseOwnedLock(ctx, projectName, scanID); err != nil {
		return err
	}
	_ = q.PublishScanEvent(ctx, projectName, ScanEvent{
		ProjectName: projectName,
		ScanID:      scanID,
		Status:      ScanStatusFailed,
		EndedAt:     &endedAt,
	})
	return nil
}

func (q *Queue) CancelScan(ctx context.Context, scanID, projectName, reason string) error {
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
	pipe.Del(ctx, keyScanRepo+projectName)
	pipe.Set(ctx, keyScanLast+projectName, scanID, scanRetention)
	pipe.ZRem(ctx, keyRunningScans, scanID)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if err := q.releaseOwnedLock(ctx, projectName, scanID); err != nil {
		return err
	}
	_ = q.PublishScanEvent(ctx, projectName, ScanEvent{
		ProjectName: projectName,
		ScanID:      scanID,
		Status:      ScanStatusCanceled,
		EndedAt:     &endedAt,
	})
	return nil
}

func (q *Queue) GetActiveScan(ctx context.Context, projectName string) (*Scan, error) {
	scanID, err := q.client.Get(ctx, keyScanRepo+projectName).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrScanNotFound
		}
		return nil, fmt.Errorf("failed to get active scan id: %w", err)
	}
	return q.GetScan(ctx, scanID)
}

func (q *Queue) GetLastScan(ctx context.Context, projectName string) (*Scan, error) {
	scanID, err := q.client.Get(ctx, keyScanLast+projectName).Result()
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
		ProjectName:       values["project"],
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
