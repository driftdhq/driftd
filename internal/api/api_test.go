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
	"github.com/driftdhq/driftd/internal/repos"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/secrets"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/driftdhq/driftd/internal/worker"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type fakeRunner struct {
	mu       sync.Mutex
	drifted  map[string]bool
	failures map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, params *runner.RunParams) (*storage.RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.failures[params.StackPath]; ok {
		return &storage.RunResult{RunAt: time.Now(), Error: err.Error()}, nil
	}

	drifted := f.drifted[params.StackPath]
	return &storage.RunResult{
		Drifted: drifted,
		RunAt:   time.Now(),
	}, nil
}

type scanResp struct {
	Stacks     []string   `json:"stacks"`
	Scan       *apiScan   `json:"scan"`
	Scans      []*apiScan `json:"scans"`
	ActiveScan *apiScan   `json:"active_scan"`
	Error      string     `json:"error"`
}

func TestScanRepoCompletesScan(t *testing.T) {
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
	if sr.Scan == nil || sr.Scan.ID == "" {
		t.Fatalf("expected scan in response")
	}
	if len(sr.Stacks) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(sr.Stacks))
	}

	scan := waitForScan(t, ts, sr.Scan.ID, 5*time.Second)
	if scan.Status != queue.ScanStatusCompleted {
		t.Fatalf("expected completed, got %s", scan.Status)
	}
	if scan.Total != 2 || scan.Completed != 2 {
		t.Fatalf("unexpected counts: total=%d completed=%d", scan.Total, scan.Completed)
	}
	if scan.Drifted != 1 || scan.Failed != 0 {
		t.Fatalf("unexpected drift/failed: drifted=%d failed=%d", scan.Drifted, scan.Failed)
	}

	// Ensure a new scan can start after completion.
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
	if sr.Scan == nil || sr.Scan.ID == "" {
		t.Fatalf("expected scan in response")
	}

	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp2.StatusCode)
	}

	_ = q.FailScan(context.Background(), sr.Scan.ID, "repo", "test cleanup")
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
	ts, q, cleanup := newTestServer(t, runner, []string{"envs/prod"}, false, nil, true)
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
	if sr.Scan == nil {
		t.Fatal("expected scan in first response")
	}
	firstScanID := sr.Scan.ID

	// Second scan should cancel the first and succeed
	resp2, err := http.Post(ts.URL+"/api/repos/repo/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	// Verify first scan was canceled
	firstScan, err := q.GetScan(context.Background(), firstScanID)
	if err != nil {
		t.Fatalf("get first scan: %v", err)
	}
	if firstScan.Status != queue.ScanStatusCanceled {
		t.Fatalf("expected first scan canceled, got %s", firstScan.Status)
	}
}

func TestScanVersionMapping(t *testing.T) {
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

	scan := getScan(t, ts, sr.Scan.ID)
	if scan.TerraformVersion != "1.6.2" {
		t.Fatalf("expected default tf version 1.6.2, got %q", scan.TerraformVersion)
	}
	if scan.TerragruntVersion != "0.56.4" {
		t.Fatalf("expected default tg version 0.56.4, got %q", scan.TerragruntVersion)
	}
	if scan.StackTFVersions["envs/prod"] != "1.5.7" {
		t.Fatalf("expected prod stack tf override, got %q", scan.StackTFVersions["envs/prod"])
	}
	if _, ok := scan.StackTFVersions["envs/dev"]; ok {
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
	keys, err := q.Client().Keys(ctx, "driftd:scan:*").Result()
	if err != nil {
		t.Fatalf("list scan keys: %v", err)
	}
	var scanKey string
	for _, key := range keys {
		if strings.HasPrefix(key, "driftd:scan:stack_scans:") || strings.HasPrefix(key, "driftd:scan:last:") {
			continue
		}
		if key == "driftd:scan:repo:repo" {
			continue
		}
		scanKey = key
		break
	}
	if scanKey == "" {
		t.Fatalf("expected scan key to exist")
	}

	status, err := q.Client().HGet(ctx, scanKey, "status").Result()
	if err != nil {
		t.Fatalf("get scan status: %v", err)
	}
	if status != queue.ScanStatusFailed {
		t.Fatalf("expected failed, got %s", status)
	}

	errMsg, err := q.Client().HGet(ctx, scanKey, "error").Result()
	if err != nil {
		t.Fatalf("get scan error: %v", err)
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
	if len(sr.Stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(sr.Stacks))
	}
	if sr.Scan == nil || sr.Scan.ID == "" {
		t.Fatalf("expected scan in response")
	}

	scan := getScan(t, ts, sr.Scan.ID)
	if scan.Status != queue.ScanStatusRunning {
		t.Fatalf("expected running, got %s", scan.Status)
	}
	if scan.Total != 1 || scan.Queued != 1 {
		t.Fatalf("unexpected counts: total=%d queued=%d", scan.Total, scan.Queued)
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
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
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
			CloneURL      string `json:"clone_url"`
			SSHURL        string `json:"ssh_url"`
			HTMLURL       string `json:"html_url"`
		}{
			Name:          "repo",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetRepo("repo").URL,
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
	if _, err := q.GetActiveScan(context.Background(), "repo"); err != queue.ErrScanNotFound {
		t.Fatalf("expected no active scan")
	}
}

func TestWebhookIgnoresUnmatchedInfraFiles(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
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
			CloneURL      string `json:"clone_url"`
			SSHURL        string `json:"ssh_url"`
			HTMLURL       string `json:"html_url"`
		}{
			Name:          "repo",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetRepo("repo").URL,
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
	if _, err := q.GetActiveScan(context.Background(), "repo"); err != queue.ErrScanNotFound {
		t.Fatalf("expected no active scan")
	}
	stacks, err := q.ListRepoStackScans(context.Background(), "repo", 10)
	if err != nil {
		t.Fatalf("list stacks: %v", err)
	}
	if len(stacks) != 0 {
		t.Fatalf("expected no stacks, got %d", len(stacks))
	}
}

func TestWebhookMatchesByCloneURLWhenNameDiffers(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
		cfg.Repos[0].Name = "configured-repo"
	})
	defer cleanup()

	payload := gitHubPushPayload{
		Ref: "refs/heads/main",
		Repository: struct {
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			CloneURL      string `json:"clone_url"`
			SSHURL        string `json:"ssh_url"`
			HTMLURL       string `json:"html_url"`
		}{
			Name:          "payload-repo",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetRepo("configured-repo").URL,
		},
		Commits: []struct {
			Added    []string `json:"added"`
			Modified []string `json:"modified"`
			Removed  []string `json:"removed"`
		}{
			{Modified: []string{"envs/prod/main.tf"}},
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sr.Scans) != 1 {
		t.Fatalf("expected exactly one scan, got %d", len(sr.Scans))
	}
	if _, err := q.GetActiveScan(context.Background(), "configured-repo"); err != nil {
		t.Fatalf("expected active scan for configured-repo: %v", err)
	}
}

func TestWebhookUsesConfiguredBranchWhenSet(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
		cfg.Repos[0].Branch = "release"
	})
	defer cleanup()

	payload := gitHubPushPayload{
		Ref: "refs/heads/release",
		Repository: struct {
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			CloneURL      string `json:"clone_url"`
			SSHURL        string `json:"ssh_url"`
			HTMLURL       string `json:"html_url"`
		}{
			Name:          "repo",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetRepo("repo").URL,
		},
		Commits: []struct {
			Added    []string `json:"added"`
			Modified []string `json:"modified"`
			Removed  []string `json:"removed"`
		}{
			{Modified: []string{"envs/prod/main.tf"}},
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if _, err := q.GetActiveScan(context.Background(), "repo"); err != nil {
		t.Fatalf("expected active scan for branch-matched repo: %v", err)
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

func TestSelectStacksForChangesIncludesRoot(t *testing.T) {
	stacks := []string{"", "envs/prod"}
	changes := []string{"envs/prod/main.tf"}
	selected := selectStacksForChanges(stacks, changes)
	if len(selected) != 2 {
		t.Fatalf("expected root + envs/prod, got %#v", selected)
	}
	if selected[0] != "" || selected[1] != "envs/prod" {
		t.Fatalf("unexpected selection order/content: %#v", selected)
	}
}

func TestTriggerPriorityDoesNotCancelScheduledOverManual(t *testing.T) {
	runner := &fakeRunner{}
	srv, _, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, nil)
	defer cleanup()

	repoCfg := srv.cfg.GetRepo("repo")
	scan, _, err := srv.startScanWithCancel(context.Background(), repoCfg, "manual", "", "")
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if _, _, err := srv.startScanWithCancel(context.Background(), repoCfg, "scheduled", "", ""); err != queue.ErrRepoLocked {
		t.Fatalf("expected repo locked, got %v", err)
	}

	scanAfter, err := srv.queue.GetScan(context.Background(), scan.ID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	if scanAfter.Status != queue.ScanStatusRunning {
		t.Fatalf("expected running, got %s", scanAfter.Status)
	}
}

func TestTriggerPriorityCancelsOnNewerManual(t *testing.T) {
	runner := &fakeRunner{}
	srv, _, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, nil)
	defer cleanup()

	repoCfg := srv.cfg.GetRepo("repo")
	first, _, err := srv.startScanWithCancel(context.Background(), repoCfg, "manual", "", "")
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}

	second, _, err := srv.startScanWithCancel(context.Background(), repoCfg, "webhook", "", "")
	if err != nil {
		t.Fatalf("start scan 2: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected new scan id")
	}

	firstScan, err := srv.queue.GetScan(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	if firstScan.Status != queue.ScanStatusCanceled {
		t.Fatalf("expected canceled, got %s", firstScan.Status)
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

func TestScanDynamicRepoViaAPI(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	_, ts, q, cleanup := newTestServerWithRepoStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, repoDir string) {
		entry := &secrets.RepoEntry{
			Name:                       "dyn-repo",
			URL:                        repoDir,
			Git:                        secrets.RepoGitConfig{Type: ""},
			Schedule:                   "",
			CancelInflightOnNewTrigger: true,
		}
		if err := store.Add(entry, nil); err != nil {
			t.Fatalf("add repo: %v", err)
		}
	}, nil)
	defer cleanup()

	body, err := json.Marshal(scanRequest{Trigger: "manual"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/repos/dyn-repo/scan", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sr scanResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sr.Scan == nil || sr.Scan.RepoName != "dyn-repo" {
		t.Fatalf("unexpected scan response: %+v", sr.Scan)
	}

	active, err := q.GetActiveScan(context.Background(), "dyn-repo")
	if err != nil || active == nil {
		t.Fatalf("expected active scan: %v", err)
	}
}

func TestSettingsAuthMiddleware(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/dev"}, false, nil, true, func(cfg *config.Config) {
		cfg.UIAuth.Username = "user"
		cfg.UIAuth.Password = "pass"
	})
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/repos", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	reqAuth, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/repos", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	reqAuth.SetBasicAuth("user", "pass")
	respAuth, err := http.DefaultClient.Do(reqAuth)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	respAuth.Body.Close()
	if respAuth.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respAuth.StatusCode)
	}
}

func TestSettingsUpdatePreservesFields(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	srv, ts, _, cleanup := newTestServerWithRepoStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, repoDir string) {
		intEntry := &secrets.IntegrationEntry{
			ID:   "int-1",
			Name: "main",
			Type: "https",
			HTTPS: &secrets.IntegrationHTTPS{
				TokenEnv: "GIT_TOKEN",
			},
		}
		if err := intStore.Add(intEntry); err != nil {
			t.Fatalf("add integration: %v", err)
		}
		entry := &secrets.RepoEntry{
			Name:                       "dyn-repo",
			URL:                        repoDir,
			Branch:                     "main",
			IgnorePaths:                []string{"modules/"},
			Schedule:                   "0 * * * *",
			CancelInflightOnNewTrigger: true,
			IntegrationID:              "int-1",
			Git:                        secrets.RepoGitConfig{},
		}
		if err := store.Add(entry, nil); err != nil {
			t.Fatalf("add repo: %v", err)
		}
	}, func(cfg *config.Config) {
		cfg.UIAuth.Username = "user"
		cfg.UIAuth.Password = "pass"
	})
	defer cleanup()

	payload := map[string]interface{}{
		"url": "https://example.com/new.git",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/repos/dyn-repo", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("user", "pass")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	entry, err := srv.repoStore.Get("dyn-repo")
	if err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if entry.URL != "https://example.com/new.git" {
		t.Fatalf("expected url updated, got %s", entry.URL)
	}
	if entry.Branch != "main" {
		t.Fatalf("expected branch preserved, got %s", entry.Branch)
	}
	if len(entry.IgnorePaths) != 1 || entry.IgnorePaths[0] != "modules/" {
		t.Fatalf("expected ignore_paths preserved, got %v", entry.IgnorePaths)
	}
	if entry.Schedule != "0 * * * *" {
		t.Fatalf("expected schedule preserved, got %s", entry.Schedule)
	}
	if entry.CancelInflightOnNewTrigger != true {
		t.Fatalf("expected cancel_inflight preserved")
	}
}

func TestSettingsAuthTypeChangeRequiresCredentials(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	_, ts, _, cleanup := newTestServerWithRepoStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, repoDir string) {
		entry := &secrets.RepoEntry{
			Name:                       "dyn-repo",
			URL:                        repoDir,
			CancelInflightOnNewTrigger: true,
			Git: secrets.RepoGitConfig{
				Type: "https",
			},
		}
		creds := &secrets.RepoCredentials{
			HTTPSUsername: "x-access-token",
			HTTPSToken:    "token",
		}
		if err := store.Add(entry, creds); err != nil {
			t.Fatalf("add repo: %v", err)
		}
	}, func(cfg *config.Config) {
		cfg.UIAuth.Username = "user"
		cfg.UIAuth.Password = "pass"
	})
	defer cleanup()

	payload := map[string]interface{}{
		"auth_type": "ssh",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/repos/dyn-repo", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("user", "pass")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
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
			ScanMaxAge:  1 * time.Minute,
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
		w = worker.New(q, r, 1, cfg, nil)
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

func newTestServerWithRepoStore(t *testing.T, r worker.Runner, stacks []string, startWorker bool, setup func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, repoDir string), mutate func(*config.Config)) (*Server, *httptest.Server, *queue.Queue, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	repoDir := createTestRepo(t, stacks, nil)

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
			ScanMaxAge:  1 * time.Minute,
			RenewEvery:  10 * time.Second,
		},
		Repos: nil,
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

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	encryptor, err := secrets.NewEncryptor(key)
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	repoStore := secrets.NewRepoStore(cfg.DataDir, encryptor)
	intStore := secrets.NewIntegrationStore(cfg.DataDir)
	if setup != nil {
		setup(repoStore, intStore, repoDir)
	}
	repoProvider := repos.NewCombinedProvider(cfg, repoStore, intStore, cfg.DataDir)

	srv, err := New(
		cfg,
		store,
		q,
		templatesFS,
		staticFS,
		WithRepoStore(repoStore),
		WithIntegrationStore(intStore),
		WithRepoProvider(repoProvider),
	)
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	server := httptest.NewServer(srv.Handler())

	var w *worker.Worker
	if startWorker {
		w = worker.New(q, r, 1, cfg, repoProvider)
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

func waitForScan(t *testing.T, ts *httptest.Server, scanID string, timeout time.Duration) *apiScan {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/scans/" + scanID)
		if err != nil {
			t.Fatalf("get scan: %v", err)
		}
		var scan apiScan
		if err := json.NewDecoder(resp.Body).Decode(&scan); err != nil {
			resp.Body.Close()
			t.Fatalf("decode scan: %v", err)
		}
		resp.Body.Close()

		if scan.Status != queue.ScanStatusRunning {
			return &scan
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("scan %s did not complete within timeout", scanID)
	return nil
}

func getScan(t *testing.T, ts *httptest.Server, scanID string) *apiScan {
	t.Helper()

	resp, err := http.Get(ts.URL + "/api/scans/" + scanID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	defer resp.Body.Close()

	var scan apiScan
	if err := json.NewDecoder(resp.Body).Decode(&scan); err != nil {
		t.Fatalf("decode scan: %v", err)
	}
	return &scan
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
