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

func TestScanDynamicProjectViaAPI(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	_, ts, q, cleanup := newTestServerWithProjectStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.ProjectStore, intStore *secrets.IntegrationStore, projectDir string) {
		entry := &secrets.ProjectEntry{
			Name:                       "dyn-project",
			URL:                        projectDir,
			Git:                        secrets.ProjectGitConfig{Type: ""},
			Schedule:                   "",
			CancelInflightOnNewTrigger: true,
		}
		if err := store.Add(entry, nil); err != nil {
			t.Fatalf("add project: %v", err)
		}
	}, nil)
	defer cleanup()

	body, err := json.Marshal(scanRequest{Trigger: "manual"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/projects/dyn-project/scan", "application/json", bytes.NewReader(body))
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
	if sr.Scan == nil || sr.Scan.ProjectName != "dyn-project" {
		t.Fatalf("unexpected scan response: %+v", sr.Scan)
	}

	active, err := q.GetActiveScan(context.Background(), "dyn-project")
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

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/projects", nil)
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

	reqAuth, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/projects", nil)
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
	srv, ts, _, cleanup := newTestServerWithProjectStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.ProjectStore, intStore *secrets.IntegrationStore, projectDir string) {
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
		entry := &secrets.ProjectEntry{
			Name:                       "dyn-project",
			URL:                        projectDir,
			Branch:                     "main",
			IgnorePaths:                []string{"modules/"},
			Schedule:                   "0 * * * *",
			CancelInflightOnNewTrigger: true,
			IntegrationID:              "int-1",
			Git:                        secrets.ProjectGitConfig{},
		}
		if err := store.Add(entry, nil); err != nil {
			t.Fatalf("add project: %v", err)
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
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/projects/dyn-project", bytes.NewReader(body))
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

	entry, err := srv.projectStore.Get("dyn-project")
	if err != nil {
		t.Fatalf("get project: %v", err)
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
	_, ts, _, cleanup := newTestServerWithProjectStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.ProjectStore, intStore *secrets.IntegrationStore, projectDir string) {
		entry := &secrets.ProjectEntry{
			Name:                       "dyn-project",
			URL:                        projectDir,
			CancelInflightOnNewTrigger: true,
			Git: secrets.ProjectGitConfig{
				Type: "https",
			},
		}
		creds := &secrets.ProjectCredentials{
			HTTPSUsername: "x-access-token",
			HTTPSToken:    "token",
		}
		if err := store.Add(entry, creds); err != nil {
			t.Fatalf("add project: %v", err)
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
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/projects/dyn-project", bytes.NewReader(body))
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

func TestSettingsProjectLifecycleAndConnectionTest(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	var projectDir string
	srv, ts, _, cleanup := newTestServerWithProjectStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.ProjectStore, intStore *secrets.IntegrationStore, sourceRepo string) {
		projectDir = sourceRepo
	}, func(cfg *config.Config) {
		cfg.UIAuth.Username = "user"
		cfg.UIAuth.Password = "pass"
	})
	defer cleanup()

	createPayload := map[string]interface{}{
		"name":           "dyn-project",
		"url":            projectDir,
		"auth_type":      "https",
		"https_token":    "token",
		"https_username": "x-access-token",
	}
	createBody, err := json.Marshal(createPayload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/projects", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("new create request: %v", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.SetBasicAuth("user", "pass")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create project request: %v", err)
	}
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from project create, got %d", createResp.StatusCode)
	}

	listReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/projects", nil)
	if err != nil {
		t.Fatalf("new list request: %v", err)
	}
	listReq.SetBasicAuth("user", "pass")
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list projects request: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from project list, got %d", listResp.StatusCode)
	}
	var reposResp []ProjectResponse
	if err := json.NewDecoder(listResp.Body).Decode(&reposResp); err != nil {
		t.Fatalf("decode project list: %v", err)
	}
	if len(reposResp) == 0 {
		t.Fatalf("expected at least one project in settings list")
	}

	testReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/projects/dyn-project/test", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("new test request: %v", err)
	}
	testReq.Header.Set("Content-Type", "application/json")
	testReq.SetBasicAuth("user", "pass")
	testResp, err := http.DefaultClient.Do(testReq)
	if err != nil {
		t.Fatalf("test project connection request: %v", err)
	}
	testResp.Body.Close()
	if testResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from project connection test, got %d", testResp.StatusCode)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/projects/dyn-project", nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	deleteReq.SetBasicAuth("user", "pass")
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete project request: %v", err)
	}
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from project delete, got %d", deleteResp.StatusCode)
	}

	if _, err := srv.projectStore.Get("dyn-project"); err != secrets.ErrProjectNotFound {
		t.Fatalf("expected deleted project to be missing, got %v", err)
	}
}

func TestSettingsIntegrationLifecycleAndReferenceProtection(t *testing.T) {
	runner := &fakeRunner{
		drifted:  map[string]bool{},
		failures: map[string]error{},
	}
	var projectDir string
	_, ts, _, cleanup := newTestServerWithProjectStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.ProjectStore, intStore *secrets.IntegrationStore, sourceRepo string) {
		projectDir = sourceRepo
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

	projectCreate := map[string]interface{}{
		"name":           "dyn-project",
		"url":            projectDir,
		"integration_id": created.ID,
	}
	projectCreateBody, err := json.Marshal(projectCreate)
	if err != nil {
		t.Fatalf("marshal project create payload: %v", err)
	}
	projectCreateReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/projects", bytes.NewReader(projectCreateBody))
	if err != nil {
		t.Fatalf("new project create request: %v", err)
	}
	projectCreateReq.Header.Set("Content-Type", "application/json")
	projectCreateReq.SetBasicAuth("user", "pass")
	projectCreateResp, err := http.DefaultClient.Do(projectCreateReq)
	if err != nil {
		t.Fatalf("create project request: %v", err)
	}
	projectCreateResp.Body.Close()
	if projectCreateResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from project create with integration, got %d", projectCreateResp.StatusCode)
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

	deleteProjectReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/projects/dyn-project", nil)
	if err != nil {
		t.Fatalf("new project delete request: %v", err)
	}
	deleteProjectReq.SetBasicAuth("user", "pass")
	deleteProjectResp, err := http.DefaultClient.Do(deleteProjectReq)
	if err != nil {
		t.Fatalf("delete project request: %v", err)
	}
	deleteProjectResp.Body.Close()
	if deleteProjectResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from project delete, got %d", deleteProjectResp.StatusCode)
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
	srv, ts, _, cleanup := newTestServerWithProjectStore(t, runner, []string{"envs/dev"}, false, func(store *secrets.ProjectStore, intStore *secrets.IntegrationStore, projectDir string) {
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
		entry := &secrets.ProjectEntry{
			Name:                       "dyn-project",
			URL:                        projectDir,
			IntegrationID:              "int-1",
			CancelInflightOnNewTrigger: true,
		}
		if err := store.Add(entry, nil); err != nil {
			t.Fatalf("add project: %v", err)
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
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/projects/dyn-project", bytes.NewReader(body))
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

	entry, err := srv.projectStore.Get("dyn-project")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if entry.IntegrationID != "" {
		t.Fatalf("expected integration_id to be cleared, got %q", entry.IntegrationID)
	}
	if entry.Git.Type != "https" {
		t.Fatalf("expected git auth type https after clearing integration, got %q", entry.Git.Type)
	}
}
