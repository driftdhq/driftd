package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/secrets"
)

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
