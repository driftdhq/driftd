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

func TestSettingsRepoLifecycleAndConnectionTest(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	var repoDir string
	srv, ts, _, cleanup := newTestServerWithRepoStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, sourceRepo string) {
		repoDir = sourceRepo
	}, func(cfg *config.Config) {
		cfg.UIAuth.Username = "user"
		cfg.UIAuth.Password = "pass"
	})
	defer cleanup()

	createPayload := map[string]interface{}{
		"name":           "dyn-repo",
		"url":            repoDir,
		"auth_type":      "https",
		"https_token":    "token",
		"https_username": "x-access-token",
	}
	createBody, err := json.Marshal(createPayload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/repos", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("new create request: %v", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.SetBasicAuth("user", "pass")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create repo request: %v", err)
	}
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from repo create, got %d", createResp.StatusCode)
	}

	listReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/repos", nil)
	if err != nil {
		t.Fatalf("new list request: %v", err)
	}
	listReq.SetBasicAuth("user", "pass")
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list repos request: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from repo list, got %d", listResp.StatusCode)
	}
	var reposResp []RepoResponse
	if err := json.NewDecoder(listResp.Body).Decode(&reposResp); err != nil {
		t.Fatalf("decode repo list: %v", err)
	}
	if len(reposResp) == 0 {
		t.Fatalf("expected at least one repo in settings list")
	}

	testReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/repos/dyn-repo/test", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("new test request: %v", err)
	}
	testReq.Header.Set("Content-Type", "application/json")
	testReq.SetBasicAuth("user", "pass")
	testResp, err := http.DefaultClient.Do(testReq)
	if err != nil {
		t.Fatalf("test repo connection request: %v", err)
	}
	testResp.Body.Close()
	if testResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from repo connection test, got %d", testResp.StatusCode)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/repos/dyn-repo", nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	deleteReq.SetBasicAuth("user", "pass")
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete repo request: %v", err)
	}
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from repo delete, got %d", deleteResp.StatusCode)
	}

	if _, err := srv.repoStore.Get("dyn-repo"); err != secrets.ErrRepoNotFound {
		t.Fatalf("expected deleted repo to be missing, got %v", err)
	}
}

func TestSettingsIntegrationLifecycleAndReferenceProtection(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	var repoDir string
	_, ts, _, cleanup := newTestServerWithRepoStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, sourceRepo string) {
		repoDir = sourceRepo
	}, func(cfg *config.Config) {
		cfg.UIAuth.Username = "user"
		cfg.UIAuth.Password = "pass"
	})
	defer cleanup()

	integrationCreate := map[string]interface{}{
		"name":            "shared-https",
		"type":            "https",
		"https_token_env": "DRIFTD_TEST_TOKEN",
	}
	createBody, err := json.Marshal(integrationCreate)
	if err != nil {
		t.Fatalf("marshal integration create payload: %v", err)
	}
	createReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/integrations", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("new integration create request: %v", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.SetBasicAuth("user", "pass")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create integration request: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from integration create, got %d", createResp.StatusCode)
	}
	var created IntegrationResponse
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created integration: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected created integration id")
	}

	listReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/integrations", nil)
	if err != nil {
		t.Fatalf("new integration list request: %v", err)
	}
	listReq.SetBasicAuth("user", "pass")
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list integrations request: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from integration list, got %d", listResp.StatusCode)
	}
	var listed []IntegrationResponse
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode integration list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one integration, got %d", len(listed))
	}

	getReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/integrations/"+created.ID, nil)
	if err != nil {
		t.Fatalf("new integration get request: %v", err)
	}
	getReq.SetBasicAuth("user", "pass")
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("get integration request: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from integration get, got %d", getResp.StatusCode)
	}

	integrationUpdate := map[string]interface{}{
		"name":            "shared-https-updated",
		"type":            "https",
		"https_token_env": "DRIFTD_TEST_TOKEN",
	}
	updateBody, err := json.Marshal(integrationUpdate)
	if err != nil {
		t.Fatalf("marshal integration update payload: %v", err)
	}
	updateReq, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/integrations/"+created.ID, bytes.NewReader(updateBody))
	if err != nil {
		t.Fatalf("new integration update request: %v", err)
	}
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.SetBasicAuth("user", "pass")
	updateResp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		t.Fatalf("update integration request: %v", err)
	}
	updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from integration update, got %d", updateResp.StatusCode)
	}

	repoCreate := map[string]interface{}{
		"name":           "dyn-repo",
		"url":            repoDir,
		"integration_id": created.ID,
	}
	repoCreateBody, err := json.Marshal(repoCreate)
	if err != nil {
		t.Fatalf("marshal repo create payload: %v", err)
	}
	repoCreateReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/repos", bytes.NewReader(repoCreateBody))
	if err != nil {
		t.Fatalf("new repo create request: %v", err)
	}
	repoCreateReq.Header.Set("Content-Type", "application/json")
	repoCreateReq.SetBasicAuth("user", "pass")
	repoCreateResp, err := http.DefaultClient.Do(repoCreateReq)
	if err != nil {
		t.Fatalf("create repo request: %v", err)
	}
	repoCreateResp.Body.Close()
	if repoCreateResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from repo create with integration, got %d", repoCreateResp.StatusCode)
	}

	deleteIntegrationReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/integrations/"+created.ID, nil)
	if err != nil {
		t.Fatalf("new integration delete request: %v", err)
	}
	deleteIntegrationReq.SetBasicAuth("user", "pass")
	deleteIntegrationResp, err := http.DefaultClient.Do(deleteIntegrationReq)
	if err != nil {
		t.Fatalf("delete integration request: %v", err)
	}
	deleteIntegrationResp.Body.Close()
	if deleteIntegrationResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when deleting referenced integration, got %d", deleteIntegrationResp.StatusCode)
	}

	deleteRepoReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/repos/dyn-repo", nil)
	if err != nil {
		t.Fatalf("new repo delete request: %v", err)
	}
	deleteRepoReq.SetBasicAuth("user", "pass")
	deleteRepoResp, err := http.DefaultClient.Do(deleteRepoReq)
	if err != nil {
		t.Fatalf("delete repo request: %v", err)
	}
	deleteRepoResp.Body.Close()
	if deleteRepoResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from repo delete, got %d", deleteRepoResp.StatusCode)
	}

	deleteIntegrationReq2, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/integrations/"+created.ID, nil)
	if err != nil {
		t.Fatalf("new integration delete request (second): %v", err)
	}
	deleteIntegrationReq2.SetBasicAuth("user", "pass")
	deleteIntegrationResp2, err := http.DefaultClient.Do(deleteIntegrationReq2)
	if err != nil {
		t.Fatalf("delete integration request (second): %v", err)
	}
	deleteIntegrationResp2.Body.Close()
	if deleteIntegrationResp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 deleting unreferenced integration, got %d", deleteIntegrationResp2.StatusCode)
	}
}

func TestSettingsUpdateCanClearIntegrationID(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	srv, ts, _, cleanup := newTestServerWithRepoStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, repoDir string) {
		intEntry := &secrets.IntegrationEntry{
			ID:   "int-1",
			Name: "shared",
			Type: "https",
			HTTPS: &secrets.IntegrationHTTPS{
				TokenEnv: "DRIFTD_TEST_TOKEN",
			},
		}
		if err := intStore.Add(intEntry); err != nil {
			t.Fatalf("add integration: %v", err)
		}
		entry := &secrets.RepoEntry{
			Name:                       "dyn-repo",
			URL:                        repoDir,
			IntegrationID:              "int-1",
			CancelInflightOnNewTrigger: true,
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
		"auth_type":      "https",
		"https_token":    "token",
		"https_username": "x-access-token",
		"integration_id": "",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
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
	if entry.IntegrationID != "" {
		t.Fatalf("expected integration_id to be cleared, got %q", entry.IntegrationID)
	}
	if entry.Git.Type != "https" {
		t.Fatalf("expected git auth type https after clearing integration, got %q", entry.Git.Type)
	}
}
