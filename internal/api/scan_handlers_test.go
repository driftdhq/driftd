package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
)

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

func TestGetStackScanAndListRepoStackScans(t *testing.T) {
	runner := &fakeRunner{}
	ts, _, cleanup := newTestServer(t, runner, []string{"envs/prod"}, false, nil, true)
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
	if len(sr.Stacks) != 1 {
		t.Fatalf("expected one stack id, got %v", sr.Stacks)
	}

	stackResp, err := http.Get(ts.URL + "/api/stacks/" + sr.Stacks[0])
	if err != nil {
		t.Fatalf("get stack scan: %v", err)
	}
	defer stackResp.Body.Close()
	if stackResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/stacks/*, got %d", stackResp.StatusCode)
	}

	var stack apiStackScan
	if err := json.NewDecoder(stackResp.Body).Decode(&stack); err != nil {
		t.Fatalf("decode stack scan: %v", err)
	}
	if stack.ID == "" || stack.ScanID != sr.Scan.ID {
		t.Fatalf("unexpected stack scan payload: %+v", stack)
	}

	listResp, err := http.Get(ts.URL + "/api/repos/repo/stacks")
	if err != nil {
		t.Fatalf("list repo stack scans: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/repos/{repo}/stacks, got %d", listResp.StatusCode)
	}

	var listed []apiStackScan
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) == 0 {
		t.Fatalf("expected at least one stack scan in list response")
	}
}
