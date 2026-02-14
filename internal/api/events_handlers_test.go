package api

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
)

func TestRepoEventsStreamEmitsSnapshotAndUpdate(t *testing.T) {
	runner := &fakeRunner{}
	ts, q, cleanup := newTestServer(t, runner, []string{"envs/dev"}, false, nil, true)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/repos/repo/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	waitForSSELine(t, reader, "event: snapshot", 3*time.Second)

	time.Sleep(150 * time.Millisecond)
	now := time.Now()
	if err := q.PublishScanEvent(context.Background(), "repo", queue.ScanEvent{
		RepoName:  "repo",
		ScanID:    "scan-1",
		Status:    queue.ScanStatusRunning,
		StartedAt: &now,
		Total:     1,
	}); err != nil {
		t.Fatalf("publish scan event: %v", err)
	}

	waitForSSELine(t, reader, "event: update", 3*time.Second)
}

func TestGlobalEventsStreamEmitsUpdate(t *testing.T) {
	runner := &fakeRunner{}
	ts, q, cleanup := newTestServer(t, runner, []string{"envs/dev"}, false, nil, true)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open global event stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	time.Sleep(150 * time.Millisecond)
	now := time.Now()
	if err := q.PublishStackEvent(context.Background(), "repo", queue.StackEvent{
		RepoName:  "repo",
		ScanID:    "scan-1",
		StackPath: "envs/dev",
		Status:    queue.StatusRunning,
		RunAt:     &now,
	}); err != nil {
		t.Fatalf("publish stack event: %v", err)
	}

	waitForSSELine(t, reader, "event: update", 3*time.Second)
}

func waitForSSELine(t *testing.T, reader *bufio.Reader, want string, timeout time.Duration) {
	t.Helper()

	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				errCh <- err
				return
			}
			if strings.TrimSpace(line) == want {
				lineCh <- line
				return
			}
		}
	}()

	select {
	case <-lineCh:
		return
	case err := <-errCh:
		t.Fatalf("read event stream: %v", err)
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for SSE line %q", want)
	}
}
