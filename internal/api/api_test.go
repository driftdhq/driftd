package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/driftdhq/driftd/internal/worker"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type fakeRunner struct {
	mu       sync.Mutex
	drifted  map[string]bool
	failures map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, repoName, repoURL, stackPath, tfVersion, tgVersion string, auth transport.AuthMethod, workspacePath string) (*runner.RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.failures[stackPath]; ok {
		return &runner.RunResult{RunAt: time.Now(), Error: err.Error()}, nil
	}

	drifted := f.drifted[stackPath]
	return &runner.RunResult{
		Drifted: drifted,
		RunAt:   time.Now(),
	}, nil
}

type scanResp struct {
	Jobs       []string    `json:"jobs"`
	Task       *queue.Task `json:"task"`
	ActiveTask *queue.Task `json:"active_task"`
	Error      string      `json:"error"`
}

func TestScanRepoCompletesTask(t *testing.T) {
	runner := &fakeRunner{
		drifted: map[string]bool{
			"envs/prod": true,
			"envs/dev":  false,
		},
	}

	ts, q, cleanup := newTestServer(t, runner, []string{"envs/prod", "envs/dev"}, true, nil, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sr.Task == nil || sr.Task.ID == "" {
		t.Fatalf("expected task in response")
	}
	if len(sr.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(sr.Jobs))
	}

	task := waitForTask(t, ts, sr.Task.ID, 5*time.Second)
	if task.Status != queue.TaskStatusCompleted {
		t.Fatalf("expected completed, got %s", task.Status)
	}
	if task.Total != 2 || task.Completed != 2 {
		t.Fatalf("unexpected counts: total=%d completed=%d", task.Total, task.Completed)
	}
	if task.Drifted != 1 || task.Failed != 0 {
		t.Fatalf("unexpected drift/failed: drifted=%d failed=%d", task.Drifted, task.Failed)
	}

	// Ensure a new task can start after completion.
	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on second scan, got %d", resp2.StatusCode)
	}

	_ = q.Close()
}

func TestScanRepoConflict(t *testing.T) {
	runner := &fakeRunner{}

	ts, q, cleanup := newTestServer(t, runner, []string{"envs/prod"}, false, nil, false)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sr.Task == nil || sr.Task.ID == "" {
		t.Fatalf("expected task in response")
	}

	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp2.StatusCode)
	}

	var sr2 scanResp
	if err := json.NewDecoder(resp2.Body).Decode(&sr2); err != nil {
		t.Fatalf("decode response 2: %v", err)
	}
	if sr2.ActiveTask == nil || sr2.ActiveTask.ID != sr.Task.ID {
		t.Fatalf("expected active_task to match original task")
	}

	_ = q.FailTask(context.Background(), sr.Task.ID, "repo", "test cleanup")
}

func TestScanRepoInvalidJSON(t *testing.T) {
	runner := &fakeRunner{}
	ts, _, cleanup := newTestServer(t, runner, []string{"envs/prod"}, false, nil, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString("{bad json"))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCancelInflightOnNewTrigger(t *testing.T) {
	runner := &fakeRunner{}
	ts, _, cleanup := newTestServer(t, runner, []string{"envs/prod"}, false, nil, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()
	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	task := getTask(t, ts, sr.Task.ID)
	if task.Status != queue.TaskStatusCanceled {
		t.Fatalf("expected canceled, got %s", task.Status)
	}
}

func TestTaskVersionMapping(t *testing.T) {
	runner := &fakeRunner{
		drifted: map[string]bool{},
	}

	versions := &testVersions{
		rootTF: "1.6.2",
		stackTF: map[string]string{
			"envs/prod": "1.5.7",
		},
		rootTG: "0.56.4",
	}

	ts, _, cleanup := newTestServer(t, runner, []string{"envs/prod", "envs/dev"}, false, versions, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	task := getTask(t, ts, sr.Task.ID)
	if task.TerraformVersion != "1.6.2" {
		t.Fatalf("expected default tf version 1.6.2, got %q", task.TerraformVersion)
	}
	if task.TerragruntVersion != "0.56.4" {
		t.Fatalf("expected default tg version 0.56.4, got %q", task.TerragruntVersion)
	}
	if task.StackTFVersions["envs/prod"] != "1.5.7" {
		t.Fatalf("expected prod stack tf override, got %q", task.StackTFVersions["envs/prod"])
	}
	if _, ok := task.StackTFVersions["envs/dev"]; ok {
		t.Fatalf("did not expect dev stack override")
	}
}

func TestScanRepoNoStacksDiscovered(t *testing.T) {
	runner := &fakeRunner{}

	ts, q, cleanup := newTestServer(t, runner, []string{}, false, nil, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}

	ctx := context.Background()
	keys, err := q.Client().Keys(ctx, "driftd:task:*").Result()
	if err != nil {
		t.Fatalf("list task keys: %v", err)
	}
	var taskKey string
	for _, key := range keys {
		if strings.HasPrefix(key, "driftd:task:jobs:") || strings.HasPrefix(key, "driftd:task:last:") {
			continue
		}
		if key == "driftd:task:repo:repo" {
			continue
		}
		taskKey = key
		break
	}
	if taskKey == "" {
		t.Fatalf("expected task key to exist")
	}

	status, err := q.Client().HGet(ctx, taskKey, "status").Result()
	if err != nil {
		t.Fatalf("get task status: %v", err)
	}
	if status != queue.TaskStatusFailed {
		t.Fatalf("expected failed, got %s", status)
	}

	errMsg, err := q.Client().HGet(ctx, taskKey, "error").Result()
	if err != nil {
		t.Fatalf("get task error: %v", err)
	}
	if errMsg != "no stacks discovered" {
		t.Fatalf("expected error message, got %q", errMsg)
	}
}

func TestScanSingleStack(t *testing.T) {
	runner := &fakeRunner{
		drifted: map[string]bool{
			"dev": false,
		},
	}

	ts, _, cleanup := newTestServer(t, runner, []string{"dev"}, false, nil, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/stacks/dev", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan stack request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(sr.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(sr.Jobs))
	}
	if sr.Task == nil || sr.Task.ID == "" {
		t.Fatalf("expected task in response")
	}

	task := getTask(t, ts, sr.Task.ID)
	if task.Status != queue.TaskStatusRunning {
		t.Fatalf("expected running, got %s", task.Status)
	}
	if task.Total != 1 || task.Queued != 1 {
		t.Fatalf("unexpected counts: total=%d queued=%d", task.Total, task.Queued)
	}
}

func TestScanStackNotFound(t *testing.T) {
	runner := &fakeRunner{}

	ts, _, cleanup := newTestServer(t, runner, []string{"envs/dev"}, false, nil, true)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/stacks/envs/prod", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan stack request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAPIAuthToken(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.APIAuth.Token = "secret"
		cfg.APIAuth.TokenHeader = "X-API-Token"
	})
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-API-Token", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIBasicAuth(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.APIAuth.Username = "driftd"
		cfg.APIAuth.Password = "change-me"
	})
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.SetBasicAuth("driftd", "change-me")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRateLimitScan(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.API.RateLimitPerMinute = 1
	})
	defer cleanup()

	resp, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	resp.Body.Close()

	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp2.StatusCode)
	}
}

func TestWebhookIgnoresNonInfraFiles(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
	})
	defer cleanup()

	payload := gitHubPushPayload{
		Ref: "refs/heads/main",
		Repository: struct {
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		}{
			Name:          "repo",
			DefaultBranch: "main",
		},
		Commits: []struct {
			Added    []string `json:"added"`
			Modified []string `json:"modified"`
			Removed  []string `json:"removed"`
		}{
			{Modified: []string{"README.md"}},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/webhooks/github", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+computeTestHMAC(body, "secret"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	if _, err := q.GetActiveTask(context.Background(), "repo"); err != queue.ErrTaskNotFound {
		t.Fatalf("expected no active task")
	}
}

func TestWebhookIgnoresUnmatchedInfraFiles(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
	})
	defer cleanup()

	payload := gitHubPushPayload{
		Ref: "refs/heads/main",
		Repository: struct {
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		}{
			Name:          "repo",
			DefaultBranch: "main",
		},
		Commits: []struct {
			Added    []string `json:"added"`
			Modified []string `json:"modified"`
			Removed  []string `json:"removed"`
		}{
			{Modified: []string{"modules/vpc/main.tf"}},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/webhooks/github", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+computeTestHMAC(body, "secret"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	if _, err := q.GetActiveTask(context.Background(), "repo"); err != queue.ErrTaskNotFound {
		t.Fatalf("expected no active task")
	}
	jobs, err := q.ListRepoJobs(context.Background(), "repo", 10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs, got %d", len(jobs))
	}
}

func TestSelectStacksForChanges(t *testing.T) {
	stacks := []string{"envs/prod", "envs/dev"}
	changes := []string{"envs/prod/main.tf"}
	selected := selectStacksForChanges(stacks, changes)
	if len(selected) != 1 || selected[0] != "envs/prod" {
		t.Fatalf("unexpected selection: %#v", selected)
	}
}

func TestTriggerPriorityDoesNotCancelScheduledOverManual(t *testing.T) {
	runner := &fakeRunner{}
	srv, _, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, nil)
	defer cleanup()

	repoCfg := srv.cfg.GetRepo("repo")
	task, _, err := srv.startTaskWithCancel(context.Background(), repoCfg, "manual", "", "")
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	if _, _, err := srv.startTaskWithCancel(context.Background(), repoCfg, "scheduled", "", ""); err != queue.ErrRepoLocked {
		t.Fatalf("expected repo locked, got %v", err)
	}

	taskAfter, err := srv.queue.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if taskAfter.Status != queue.TaskStatusRunning {
		t.Fatalf("expected running, got %s", taskAfter.Status)
	}
}

func TestTriggerPriorityCancelsOnNewerManual(t *testing.T) {
	runner := &fakeRunner{}
	srv, _, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, nil)
	defer cleanup()

	repoCfg := srv.cfg.GetRepo("repo")
	first, _, err := srv.startTaskWithCancel(context.Background(), repoCfg, "manual", "", "")
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	second, _, err := srv.startTaskWithCancel(context.Background(), repoCfg, "webhook", "", "")
	if err != nil {
		t.Fatalf("start task 2: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected new task id")
	}

	firstTask, err := srv.queue.GetTask(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if firstTask.Status != queue.TaskStatusCanceled {
		t.Fatalf("expected canceled, got %s", firstTask.Status)
	}
}

func TestIsInfraFile(t *testing.T) {
	cases := map[string]bool{
		"main.tf":            true,
		"vars.tf.json":       true,
		"env.tfvars":         true,
		"env.tfvars.json":    true,
		"terragrunt.hcl":     true,
		"modules/app.hcl":    true,
		"README.md":          false,
		"scripts/deploy.sh":  false,
		"config.yaml":        false,
		"module/outputs.txt": false,
	}
	for path, want := range cases {
		if got := isInfraFile(path); got != want {
			t.Fatalf("isInfraFile(%q)=%v, want %v", path, got, want)
		}
	}
}

func newTestServer(t *testing.T, r worker.Runner, stacks []string, startWorker bool, versions *testVersions, cancelInflight bool) (*httptest.Server, *queue.Queue, func()) {
	t.Helper()
	_, server, q, cleanup := newTestServerWithConfig(t, r, stacks, startWorker, versions, cancelInflight, nil)
	return server, q, cleanup
}

func newTestServerWithConfig(t *testing.T, r worker.Runner, stacks []string, startWorker bool, versions *testVersions, cancelInflight bool, mutate func(*config.Config)) (*Server, *httptest.Server, *queue.Queue, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	repoDir := createTestRepo(t, stacks, versions)

	cancelInflightFlag := cancelInflight

	cfg := &config.Config{
		DataDir: t.TempDir(),
		Redis: config.RedisConfig{
			Addr: mr.Addr(),
			DB:   0,
		},
		Worker: config.WorkerConfig{
			Concurrency: 1,
			LockTTL:     2 * time.Minute,
			RetryOnce:   false,
			TaskMaxAge:  1 * time.Minute,
			RenewEvery:  10 * time.Second,
		},
		Repos: []config.RepoConfig{
			{
				Name:                       "repo",
				URL:                        repoDir,
				CancelInflightOnNewTrigger: &cancelInflightFlag,
			},
		},
	}

	if mutate != nil {
		mutate(cfg)
	}

	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Worker.LockTTL)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}

	store := storage.New(cfg.DataDir)
	templatesFS := os.DirFS("testdata")
	staticFS := os.DirFS("testdata")

	srv, err := New(cfg, store, q, templatesFS, staticFS)
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	server := httptest.NewServer(srv.Handler())

	var w *worker.Worker
	if startWorker {
		w = worker.New(q, r, 1, cfg)
		w.Start()
	}

	cleanup := func() {
		if w != nil {
			w.Stop()
		}
		server.Close()
		_ = q.Close()
		mr.Close()
	}

	return srv, server, q, cleanup
}

func waitForTask(t *testing.T, ts *httptest.Server, taskID string, timeout time.Duration) *queue.Task {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/tasks/" + taskID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		var task queue.Task
		if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
			resp.Body.Close()
			t.Fatalf("decode task: %v", err)
		}
		resp.Body.Close()

		if task.Status != queue.TaskStatusRunning {
			return &task
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("task %s did not complete within timeout", taskID)
	return nil
}

func getTask(t *testing.T, ts *httptest.Server, taskID string) *queue.Task {
	t.Helper()

	resp, err := http.Get(ts.URL + "/api/tasks/" + taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	defer resp.Body.Close()

	var task queue.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return &task
}

type testVersions struct {
	rootTF  string
	rootTG  string
	stackTF map[string]string
	stackTG map[string]string
}

func createTestRepo(t *testing.T, stacks []string, versions *testVersions) string {
	t.Helper()

	dir := t.TempDir()
	if versions != nil {
		if versions.rootTF != "" {
			if err := os.WriteFile(filepath.Join(dir, ".terraform-version"), []byte(versions.rootTF), 0644); err != nil {
				t.Fatalf("write root tf version: %v", err)
			}
		}
		if versions.rootTG != "" {
			if err := os.WriteFile(filepath.Join(dir, ".terragrunt-version"), []byte(versions.rootTG), 0644); err != nil {
				t.Fatalf("write root tg version: %v", err)
			}
		}
	}
	for _, stack := range stacks {
		path := filepath.Join(dir, stack)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatalf("mkdir stack: %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, "main.tf"), []byte(""), 0644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		if versions != nil {
			if v := versions.stackTF[stack]; v != "" {
				if err := os.WriteFile(filepath.Join(path, ".terraform-version"), []byte(v), 0644); err != nil {
					t.Fatalf("write stack tf version: %v", err)
				}
			}
			if v := versions.stackTG[stack]; v != "" {
				if err := os.WriteFile(filepath.Join(path, ".terragrunt-version"), []byte(v), 0644); err != nil {
					t.Fatalf("write stack tg version: %v", err)
				}
			}
		}
	}
	if len(stacks) == 0 {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("empty repo"), 0644); err != nil {
			t.Fatalf("write placeholder: %v", err)
		}
	}

	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return dir
}

func computeTestHMAC(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
