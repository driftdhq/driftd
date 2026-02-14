package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const projectEventsPrefix = "driftd:events:"

type ProjectEvent struct {
	Type        string     `json:"type"`
	ProjectName string     `json:"project"`
	ScanID      string     `json:"scan_id,omitempty"`
	CommitSHA   string     `json:"commit_sha,omitempty"`
	StackPath   string     `json:"stack_path,omitempty"`
	Status      string     `json:"status,omitempty"`
	Drifted     *bool      `json:"drifted,omitempty"`
	Error       string     `json:"error,omitempty"`
	RunAt       *time.Time `json:"run_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Completed   int        `json:"completed,omitempty"`
	Failed      int        `json:"failed,omitempty"`
	Total       int        `json:"total,omitempty"`
	DriftedCnt  int        `json:"drifted_count,omitempty"`
	Timestamp   time.Time  `json:"timestamp"`
}

type ScanEvent struct {
	ProjectName string
	ScanID      string
	CommitSHA   string
	Status      string
	Completed   int
	Failed      int
	Total       int
	DriftedCnt  int
	StartedAt   *time.Time
	EndedAt     *time.Time
}

type StackEvent struct {
	ProjectName string
	ScanID      string
	StackPath   string
	Status      string
	Drifted     *bool
	Error       string
	RunAt       *time.Time
}

func (e ScanEvent) ToProjectEvent() ProjectEvent {
	return ProjectEvent{
		Type:        "scan_update",
		ProjectName: e.ProjectName,
		ScanID:      e.ScanID,
		CommitSHA:   e.CommitSHA,
		Status:      e.Status,
		Completed:   e.Completed,
		Failed:      e.Failed,
		Total:       e.Total,
		DriftedCnt:  e.DriftedCnt,
		StartedAt:   e.StartedAt,
		EndedAt:     e.EndedAt,
	}
}

func (e StackEvent) ToProjectEvent() ProjectEvent {
	return ProjectEvent{
		Type:        "stack_update",
		ProjectName: e.ProjectName,
		ScanID:      e.ScanID,
		StackPath:   e.StackPath,
		Status:      e.Status,
		Drifted:     e.Drifted,
		Error:       e.Error,
		RunAt:       e.RunAt,
	}
}

func (q *Queue) PublishEvent(ctx context.Context, projectName string, event ProjectEvent) error {
	if projectName == "" {
		return nil
	}
	event.ProjectName = projectName
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return q.client.Publish(ctx, projectEventsPrefix+projectName, data).Err()
}

func (q *Queue) PublishScanEvent(ctx context.Context, projectName string, event ScanEvent) error {
	if projectName == "" {
		projectName = event.ProjectName
	}
	return q.PublishEvent(ctx, projectName, event.ToProjectEvent())
}

func (q *Queue) PublishStackEvent(ctx context.Context, projectName string, event StackEvent) error {
	if projectName == "" {
		projectName = event.ProjectName
	}
	return q.PublishEvent(ctx, projectName, event.ToProjectEvent())
}
