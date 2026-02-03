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
	StackPath  string     `json:"stack_path,omitempty"`
	Status     string     `json:"status,omitempty"`
	Drifted    *bool      `json:"drifted,omitempty"`
	Error      string     `json:"error,omitempty"`
	RunAt      *time.Time `json:"run_at,omitempty"`
	Completed  int        `json:"completed,omitempty"`
	Failed     int        `json:"failed,omitempty"`
	Total      int        `json:"total,omitempty"`
	DriftedCnt int        `json:"drifted_count,omitempty"`
	Timestamp  time.Time  `json:"timestamp"`
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
