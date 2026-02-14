package api

import (
	"encoding/json"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/storage"
)

type ssePayload struct {
	Kind        string                `json:"kind"`
	Type        string                `json:"type,omitempty"`
	Project     string                `json:"project"`
	ScanID      string                `json:"scan_id,omitempty"`
	CommitSHA   string                `json:"commit_sha,omitempty"`
	StackPath   string                `json:"stack_path,omitempty"`
	Status      string                `json:"status,omitempty"`
	StatusLabel string                `json:"status_label,omitempty"`
	IsTerminal  bool                  `json:"is_terminal,omitempty"`
	ProgressPct int                   `json:"progress_pct"`
	Completed   int                   `json:"completed,omitempty"`
	Failed      int                   `json:"failed,omitempty"`
	Total       int                   `json:"total,omitempty"`
	Drifted     *bool                 `json:"drifted,omitempty"`
	Error       string                `json:"error,omitempty"`
	RunAt       *time.Time            `json:"run_at,omitempty"`
	StartedAt   *time.Time            `json:"started_at,omitempty"`
	EndedAt     *time.Time            `json:"ended_at,omitempty"`
	ActiveScan  *scanSummary          `json:"active_scan,omitempty"`
	LastScan    *scanSummary          `json:"last_scan,omitempty"`
	Stacks      []storage.StackStatus `json:"stacks,omitempty"`
}

type scanSummary struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	StatusLabel string     `json:"status_label"`
	Completed   int        `json:"completed"`
	Failed      int        `json:"failed"`
	Total       int        `json:"total"`
	ProgressPct int        `json:"progress_pct"`
	IsTerminal  bool       `json:"is_terminal"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	CommitSHA   string     `json:"commit_sha,omitempty"`
}

func buildSnapshotPayload(project string, active, last *queue.Scan, stacks []storage.StackStatus) ([]byte, error) {
	payload := ssePayload{
		Kind:       "snapshot",
		Project:    project,
		ActiveScan: buildScanSummary(active),
		LastScan:   buildScanSummary(last),
		Stacks:     stacks,
	}
	return json.Marshal(payload)
}

func buildUpdatePayload(event *queue.ProjectEvent) ([]byte, error) {
	if event == nil {
		return json.Marshal(ssePayload{Kind: "unknown"})
	}
	payload := ssePayload{
		Type:      event.Type,
		Project:   event.ProjectName,
		ScanID:    event.ScanID,
		CommitSHA: event.CommitSHA,
		StackPath: event.StackPath,
		Status:    event.Status,
		Completed: event.Completed,
		Failed:    event.Failed,
		Total:     event.Total,
		Drifted:   event.Drifted,
		Error:     event.Error,
		RunAt:     event.RunAt,
		StartedAt: event.StartedAt,
		EndedAt:   event.EndedAt,
	}

	switch event.Type {
	case "scan_update":
		payload.Kind = "scan"
		payload.StatusLabel = scanStatusLabel(event.Status)
		payload.IsTerminal = isTerminalScan(event.Status)
		payload.ProgressPct = progressPct(event.Completed, event.Failed, event.Total)
	case "stack_update":
		payload.Kind = "stack"
		payload.StatusLabel = stackStatusLabel(event.Status, event.Drifted, event.Error)
		payload.IsTerminal = isTerminalStack(event.Status)
	default:
		payload.Kind = "unknown"
	}

	return json.Marshal(payload)
}

func buildScanSummary(scan *queue.Scan) *scanSummary {
	if scan == nil {
		return nil
	}
	var endedAt *time.Time
	if !scan.EndedAt.IsZero() {
		endedAt = &scan.EndedAt
	}
	return &scanSummary{
		ID:          scan.ID,
		Status:      scan.Status,
		StatusLabel: scanStatusLabel(scan.Status),
		Completed:   scan.Completed,
		Failed:      scan.Failed,
		Total:       scan.Total,
		ProgressPct: progressPct(scan.Completed, scan.Failed, scan.Total),
		IsTerminal:  isTerminalScan(scan.Status),
		StartedAt:   &scan.StartedAt,
		EndedAt:     endedAt,
		CommitSHA:   scan.CommitSHA,
	}
}

func progressPct(completed, failed, total int) int {
	if total <= 0 {
		return 0
	}
	done := completed + failed
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	return int((done * 100) / total)
}

func isTerminalScan(status string) bool {
	switch status {
	case queue.ScanStatusCompleted, queue.ScanStatusFailed, queue.ScanStatusCanceled:
		return true
	default:
		return false
	}
}

func isTerminalStack(status string) bool {
	switch status {
	case queue.StatusCompleted, queue.StatusFailed, queue.StatusCanceled:
		return true
	default:
		return false
	}
}

func scanStatusLabel(status string) string {
	switch status {
	case queue.ScanStatusRunning:
		return "Running"
	case queue.ScanStatusCompleted:
		return "Completed"
	case queue.ScanStatusFailed:
		return "Failed"
	case queue.ScanStatusCanceled:
		return "Canceled"
	default:
		return "Unknown"
	}
}

func stackStatusLabel(status string, drifted *bool, errMsg string) string {
	if status == queue.StatusRunning {
		return "Running"
	}
	if status == queue.StatusFailed || errMsg != "" {
		return "Error"
	}
	if drifted != nil && *drifted {
		return "Drifted"
	}
	return "Healthy"
}
