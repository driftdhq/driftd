package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
)

func TestBuildUpdatePayloadScan(t *testing.T) {
	event := &queue.RepoEvent{
		Type:      "scan_update",
		RepoName:  "repo",
		ScanID:    "scan-1",
		Status:    queue.ScanStatusRunning,
		Completed: 3,
		Failed:    1,
		Total:     10,
	}

	data, err := buildUpdatePayload(event)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	var payload ssePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Kind != "scan" {
		t.Fatalf("expected scan kind, got %s", payload.Kind)
	}
	if payload.ProgressPct != 40 {
		t.Fatalf("expected progress 40, got %d", payload.ProgressPct)
	}
	if payload.StatusLabel != "Running" {
		t.Fatalf("expected status label Running, got %s", payload.StatusLabel)
	}
}

func TestBuildUpdatePayloadStack(t *testing.T) {
	event := &queue.RepoEvent{
		Type:      "stack_update",
		RepoName:  "repo",
		ScanID:    "scan-1",
		StackPath: "envs/dev",
		Status:    queue.StatusFailed,
		Error:     "boom",
	}

	data, err := buildUpdatePayload(event)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	var payload ssePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Kind != "stack" {
		t.Fatalf("expected stack kind, got %s", payload.Kind)
	}
	if payload.StatusLabel != "Error" {
		t.Fatalf("expected status label Error, got %s", payload.StatusLabel)
	}
}

func TestProgressPctZeroInJSON(t *testing.T) {
	event := &queue.RepoEvent{
		Type:      "scan_update",
		RepoName:  "repo",
		ScanID:    "scan-1",
		Status:    queue.ScanStatusRunning,
		Completed: 0,
		Failed:    0,
		Total:     10,
	}

	data, err := buildUpdatePayload(event)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	pctRaw, ok := raw["progress_pct"]
	if !ok {
		t.Fatal("progress_pct missing from JSON (should not be omitted when zero)")
	}
	if string(pctRaw) != "0" {
		t.Fatalf("expected progress_pct=0, got %s", string(pctRaw))
	}
}

func TestSnapshotPayloadIncludesProgressPct(t *testing.T) {
	scan := &queue.Scan{
		ID:        "scan-1",
		RepoName:  "repo",
		Status:    queue.ScanStatusRunning,
		StartedAt: time.Now(),
		Total:     10,
	}

	data, err := buildSnapshotPayload("repo", scan, nil, nil)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	activeScanRaw, ok := raw["active_scan"]
	if !ok {
		t.Fatal("active_scan missing from snapshot")
	}

	var activeScan map[string]json.RawMessage
	if err := json.Unmarshal(activeScanRaw, &activeScan); err != nil {
		t.Fatalf("unmarshal active_scan: %v", err)
	}
	pctRaw, ok := activeScan["progress_pct"]
	if !ok {
		t.Fatal("progress_pct missing from active_scan in snapshot")
	}
	if string(pctRaw) != "0" {
		t.Fatalf("expected progress_pct=0, got %s", string(pctRaw))
	}
}

func TestProgressPct(t *testing.T) {
	tests := []struct {
		name      string
		completed int
		failed    int
		total     int
		want      int
	}{
		{"normal", 3, 1, 10, 40},
		{"zero total", 0, 0, 0, 0},
		{"negative total", 0, 0, -1, 0},
		{"done exceeds total", 15, 0, 10, 100},
		{"negative done clamped", -5, 0, 10, 0},
		{"all complete", 10, 0, 10, 100},
		{"all failed", 0, 10, 10, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := progressPct(tt.completed, tt.failed, tt.total)
			if got != tt.want {
				t.Errorf("progressPct(%d, %d, %d) = %d, want %d", tt.completed, tt.failed, tt.total, got, tt.want)
			}
		})
	}
}

func TestScanStatusLabel(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{queue.ScanStatusRunning, "Running"},
		{queue.ScanStatusCompleted, "Completed"},
		{queue.ScanStatusFailed, "Failed"},
		{queue.ScanStatusCanceled, "Canceled"},
		{"bogus", "Unknown"},
		{"", "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := scanStatusLabel(tt.status); got != tt.want {
				t.Errorf("scanStatusLabel(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestStackStatusLabel(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }

	tests := []struct {
		name    string
		status  string
		drifted *bool
		errMsg  string
		want    string
	}{
		{"running", queue.StatusRunning, nil, "", "Running"},
		{"failed status", queue.StatusFailed, nil, "", "Error"},
		{"error message", queue.StatusCompleted, nil, "something broke", "Error"},
		{"drifted", queue.StatusCompleted, boolPtr(true), "", "Drifted"},
		{"healthy", queue.StatusCompleted, boolPtr(false), "", "Healthy"},
		{"healthy nil drifted", queue.StatusCompleted, nil, "", "Healthy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stackStatusLabel(tt.status, tt.drifted, tt.errMsg); got != tt.want {
				t.Errorf("stackStatusLabel(%q, %v, %q) = %q, want %q", tt.status, tt.drifted, tt.errMsg, got, tt.want)
			}
		})
	}
}

func TestBuildScanSummaryNil(t *testing.T) {
	if got := buildScanSummary(nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestBuildScanSummaryZeroEndedAt(t *testing.T) {
	scan := &queue.Scan{
		ID:        "scan-1",
		Status:    queue.ScanStatusRunning,
		StartedAt: time.Now(),
		Total:     5,
	}
	summary := buildScanSummary(scan)
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.EndedAt != nil {
		t.Fatalf("expected nil EndedAt for zero time, got %v", summary.EndedAt)
	}
	if summary.ProgressPct != 0 {
		t.Fatalf("expected progress_pct=0, got %d", summary.ProgressPct)
	}
}
