package api

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/driftdhq/driftd/internal/config"
)

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

	resp, err := http.Post(ts.URL+"/api/projects/project/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request failed: %v", err)
	}
	resp.Body.Close()

	resp2, err := http.Post(ts.URL+"/api/projects/project/scan", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("scan request 2 failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp2.StatusCode)
	}
}

func TestAPIWriteTokenBlocksReadTokenOnMutations(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.APIAuth.Token = "read-token"
		cfg.APIAuth.TokenHeader = "X-API-Token"
		cfg.APIAuth.WriteToken = "write-token"
		cfg.APIAuth.WriteTokenHeader = "X-API-Write-Token"
	})
	defer cleanup()

	readReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	readReq.Header.Set("X-API-Token", "read-token")
	readResp, err := http.DefaultClient.Do(readReq)
	if err != nil {
		t.Fatalf("read request failed: %v", err)
	}
	readResp.Body.Close()
	if readResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for read auth, got %d", readResp.StatusCode)
	}

	writeReqWithReadToken, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/project/scan", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	writeReqWithReadToken.Header.Set("Content-Type", "application/json")
	writeReqWithReadToken.Header.Set("X-API-Token", "read-token")
	writeRespWithReadToken, err := http.DefaultClient.Do(writeReqWithReadToken)
	if err != nil {
		t.Fatalf("write request with read token failed: %v", err)
	}
	writeRespWithReadToken.Body.Close()
	if writeRespWithReadToken.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for write with read token, got %d", writeRespWithReadToken.StatusCode)
	}

	writeReqWithWriteToken, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/project/scan", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	writeReqWithWriteToken.Header.Set("Content-Type", "application/json")
	writeReqWithWriteToken.Header.Set("X-API-Write-Token", "write-token")
	writeRespWithWriteToken, err := http.DefaultClient.Do(writeReqWithWriteToken)
	if err != nil {
		t.Fatalf("write request with write token failed: %v", err)
	}
	writeRespWithWriteToken.Body.Close()
	if writeRespWithWriteToken.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for write with write token, got %d", writeRespWithWriteToken.StatusCode)
	}
}

func TestRateLimitSettingsWrites(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.API.RateLimitPerMinute = 1
	})
	defer cleanup()

	body := bytes.NewBufferString(`{"name":"p","url":"https://example.com/repo.git","auth_type":"https"}`)
	resp, err := http.Post(ts.URL+"/api/settings/projects", "application/json", body)
	if err != nil {
		t.Fatalf("settings write failed: %v", err)
	}
	resp.Body.Close()

	body2 := bytes.NewBufferString(`{"name":"p2","url":"https://example.com/repo.git","auth_type":"https"}`)
	resp2, err := http.Post(ts.URL+"/api/settings/projects", "application/json", body2)
	if err != nil {
		t.Fatalf("settings write 2 failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for second settings write, got %d", resp2.StatusCode)
	}
}

func TestSecurityHeadersApplied(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, nil)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Fatalf("expected Content-Security-Policy header")
	}
}

func TestExternalAuthRoleMapping(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Auth.Mode = "external"
		cfg.Auth.External.DefaultRole = "viewer"
		cfg.Auth.External.Roles.Operators = []string{"platform-operators"}
		cfg.Auth.External.Roles.Admins = []string{"platform-admins"}
	})
	defer cleanup()

	listReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/projects/project/stacks", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	listReq.Header.Set("X-Auth-Request-User", "alice")
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("viewer list request failed: %v", err)
	}
	listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for viewer read, got %d", listResp.StatusCode)
	}

	viewerScanReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/project/scan", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	viewerScanReq.Header.Set("X-Auth-Request-User", "alice")
	viewerScanReq.Header.Set("Content-Type", "application/json")
	viewerScanResp, err := http.DefaultClient.Do(viewerScanReq)
	if err != nil {
		t.Fatalf("viewer scan request failed: %v", err)
	}
	viewerScanResp.Body.Close()
	if viewerScanResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer scan write, got %d", viewerScanResp.StatusCode)
	}

	operatorScanReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/project/scan", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	operatorScanReq.Header.Set("X-Auth-Request-User", "bob")
	operatorScanReq.Header.Set("X-Auth-Request-Groups", "platform-operators")
	operatorScanReq.Header.Set("Content-Type", "application/json")
	operatorScanResp, err := http.DefaultClient.Do(operatorScanReq)
	if err != nil {
		t.Fatalf("operator scan request failed: %v", err)
	}
	operatorScanResp.Body.Close()
	if operatorScanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for operator scan write, got %d", operatorScanResp.StatusCode)
	}

	operatorSettingsReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/projects", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	operatorSettingsReq.Header.Set("X-Auth-Request-User", "bob")
	operatorSettingsReq.Header.Set("X-Auth-Request-Groups", "platform-operators")
	operatorSettingsResp, err := http.DefaultClient.Do(operatorSettingsReq)
	if err != nil {
		t.Fatalf("operator settings request failed: %v", err)
	}
	operatorSettingsResp.Body.Close()
	if operatorSettingsResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for operator settings read, got %d", operatorSettingsResp.StatusCode)
	}

	adminSettingsReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/projects", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	adminSettingsReq.Header.Set("X-Auth-Request-User", "carol")
	adminSettingsReq.Header.Set("X-Auth-Request-Groups", "platform-admins")
	adminSettingsResp, err := http.DefaultClient.Do(adminSettingsReq)
	if err != nil {
		t.Fatalf("admin settings request failed: %v", err)
	}
	adminSettingsResp.Body.Close()
	if adminSettingsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for admin settings read, got %d", adminSettingsResp.StatusCode)
	}
}

func TestExternalAuthRequiresIdentityHeaders(t *testing.T) {
	runner := &fakeRunner{}
	_, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Auth.Mode = "external"
		cfg.Auth.External.DefaultRole = "viewer"
	})
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/projects/project/stacks", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without external identity headers, got %d", resp.StatusCode)
	}

	healthResp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /api/health in external mode, got %d", healthResp.StatusCode)
	}
}
