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
