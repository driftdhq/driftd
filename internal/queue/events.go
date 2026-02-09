package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const repoEventsPrefix = "driftd:events:"

type RepoEvent struct {
	Type       string     `json:"type"`
	RepoName   string     `json:"repo"`
	ScanID     string     `json:"scan_id,omitempty"`
	CommitSHA  string     `json:"commit_sha,omitempty"`
	StackPath  string     `json:"stack_path,omitempty"`
	Status     string     `json:"status,omitempty"`
	Drifted    *bool      `json:"drifted,omitempty"`
	Error      string     `json:"error,omitempty"`
	RunAt      *time.Time `json:"run_at,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Completed  int        `json:"completed,omitempty"`
	Failed     int        `json:"failed,omitempty"`
	Total      int        `json:"total,omitempty"`
	DriftedCnt int        `json:"drifted_count,omitempty"`
	Timestamp  time.Time  `json:"timestamp"`
}

type ScanEvent struct {
	RepoName   string
	ScanID     string
	CommitSHA  string
	Status     string
	Completed  int
	Failed     int
	Total      int
	DriftedCnt int
	StartedAt  *time.Time
	EndedAt    *time.Time
}

type StackEvent struct {
	RepoName  string
	ScanID    string
	StackPath string
	Status    string
	Drifted   *bool
	Error     string
	RunAt     *time.Time
}

func (e ScanEvent) ToRepoEvent() RepoEvent {
	return RepoEvent{
		Type:       "scan_update",
		RepoName:   e.RepoName,
		ScanID:     e.ScanID,
		CommitSHA:  e.CommitSHA,
		Status:     e.Status,
		Completed:  e.Completed,
		Failed:     e.Failed,
		Total:      e.Total,
		DriftedCnt: e.DriftedCnt,
		StartedAt:  e.StartedAt,
		EndedAt:    e.EndedAt,
	}
}

func (e StackEvent) ToRepoEvent() RepoEvent {
	return RepoEvent{
		Type:      "stack_update",
		RepoName:  e.RepoName,
		ScanID:    e.ScanID,
		StackPath: e.StackPath,
		Status:    e.Status,
		Drifted:   e.Drifted,
		Error:     e.Error,
		RunAt:     e.RunAt,
	}
}

func (q *Queue) PublishEvent(ctx context.Context, repoName string, event RepoEvent) error {
	if repoName == "" {
		return nil
	}
	event.RepoName = repoName
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return q.client.Publish(ctx, repoEventsPrefix+repoName, data).Err()
}

func (q *Queue) PublishScanEvent(ctx context.Context, repoName string, event ScanEvent) error {
	if repoName == "" {
		repoName = event.RepoName
	}
	return q.PublishEvent(ctx, repoName, event.ToRepoEvent())
}

func (q *Queue) PublishStackEvent(ctx context.Context, repoName string, event StackEvent) error {
	if repoName == "" {
		repoName = event.RepoName
	}
	return q.PublishEvent(ctx, repoName, event.ToRepoEvent())
}
