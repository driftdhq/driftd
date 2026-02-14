package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
)

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
			Name:          "project",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetProject("project").URL,
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
	if _, err := q.GetActiveScan(context.Background(), "project"); err != queue.ErrScanNotFound {
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
			Name:          "project",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetProject("project").URL,
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
	if _, err := q.GetActiveScan(context.Background(), "project"); err != queue.ErrScanNotFound {
		t.Fatalf("expected no active scan")
	}
	stacks, err := q.ListProjectStackScans(context.Background(), "project", 10)
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
		cfg.Projects[0].Name = "configured-project"
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
			Name:          "payload-project",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetProject("configured-project").URL,
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
	if _, err := q.GetActiveScan(context.Background(), "configured-project"); err != nil {
		t.Fatalf("expected active scan for configured-project: %v", err)
	}
}

func TestWebhookUsesConfiguredBranchWhenSet(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
		cfg.Projects[0].Branch = "release"
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
			Name:          "project",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetProject("project").URL,
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
	if _, err := q.GetActiveScan(context.Background(), "project"); err != nil {
		t.Fatalf("expected active scan for branch-matched project: %v", err)
	}
}

func TestWebhookMonoprojectPrefiltersByRootPath(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"aws/dev/envs/prod", "aws/staging/envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
		baseURL := cfg.Projects[0].URL
		cancelInflight := true
		cfg.Projects = []config.ProjectConfig{
			{
				Name:                       "aws-dev",
				URL:                        baseURL,
				CloneURL:                   baseURL,
				RootPath:                   "aws/dev",
				CancelInflightOnNewTrigger: &cancelInflight,
			},
			{
				Name:                       "aws-staging",
				URL:                        baseURL,
				CloneURL:                   baseURL,
				RootPath:                   "aws/staging",
				CancelInflightOnNewTrigger: &cancelInflight,
			},
		}
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
			Name:          "infra-monorepo",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetProject("aws-dev").URL,
		},
		Commits: []struct {
			Added    []string `json:"added"`
			Modified []string `json:"modified"`
			Removed  []string `json:"removed"`
		}{
			{Modified: []string{"aws/dev/envs/prod/main.tf"}},
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
		t.Fatalf("expected exactly one project scan, got %d", len(sr.Scans))
	}
	if sr.Scans[0].ProjectName != "aws-dev" {
		t.Fatalf("expected aws-dev scan, got %s", sr.Scans[0].ProjectName)
	}

	if _, err := q.GetActiveScan(context.Background(), "aws-dev"); err != nil {
		t.Fatalf("expected active scan for aws-dev: %v", err)
	}
	if _, err := q.GetActiveScan(context.Background(), "aws-staging"); err != queue.ErrScanNotFound {
		t.Fatalf("expected no scan for aws-staging, got %v", err)
	}
	stagingStacks, err := q.ListProjectStackScans(context.Background(), "aws-staging", 10)
	if err != nil {
		t.Fatalf("list staging stacks: %v", err)
	}
	if len(stagingStacks) != 0 {
		t.Fatalf("expected no staging stacks, got %d", len(stagingStacks))
	}
}

func TestGetReposByURLMatchesSSHAndHTMLForms(t *testing.T) {
	runner := &fakeRunner{}
	srv, _, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Projects[0].Name = "github-project"
		cfg.Projects[0].URL = "https://github.com/example/infra.git"
		cfg.Projects[0].CloneURL = cfg.Projects[0].URL
	})
	defer cleanup()

	sshMatches, err := srv.getReposByURL("", "git@github.com:example/infra.git", "")
	if err != nil {
		t.Fatalf("ssh lookup failed: %v", err)
	}
	if len(sshMatches) != 1 || sshMatches[0].Name != "github-project" {
		t.Fatalf("expected github-project for ssh lookup, got %#v", sshMatches)
	}

	htmlMatches, err := srv.getReposByURL("", "", "https://github.com/example/infra")
	if err != nil {
		t.Fatalf("html lookup failed: %v", err)
	}
	if len(htmlMatches) != 1 || htmlMatches[0].Name != "github-project" {
		t.Fatalf("expected github-project for html lookup, got %#v", htmlMatches)
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

func TestWebhookBranchMismatchReturnsAcceptedWithoutScan(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
		cfg.Projects[0].Branch = "release"
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
			Name:          "project",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetProject("project").URL,
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
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	if _, err := q.GetActiveScan(context.Background(), "project"); err != queue.ErrScanNotFound {
		t.Fatalf("expected no active scan on branch mismatch")
	}
}

func TestProjectMatchesWebhookBranch(t *testing.T) {
	tests := []struct {
		name                 string
		projectBranch        string
		payloadBranch        string
		payloadDefaultBranch string
		want                 bool
	}{
		{
			name:                 "uses configured branch",
			projectBranch:        "release",
			payloadBranch:        "release",
			payloadDefaultBranch: "main",
			want:                 true,
		},
		{
			name:                 "configured branch mismatch",
			projectBranch:        "release",
			payloadBranch:        "main",
			payloadDefaultBranch: "main",
			want:                 false,
		},
		{
			name:                 "falls back to payload default branch",
			projectBranch:        "",
			payloadBranch:        "main",
			payloadDefaultBranch: "main",
			want:                 true,
		},
		{
			name:                 "no configured or default branch accepts",
			projectBranch:        "",
			payloadBranch:        "feature/foo",
			payloadDefaultBranch: "",
			want:                 true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectCfg := &config.ProjectConfig{Branch: tc.projectBranch}
			if got := projectMatchesWebhookBranch(projectCfg, tc.payloadBranch, tc.payloadDefaultBranch); got != tc.want {
				t.Fatalf("projectMatchesWebhookBranch() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProjectPathMatchesWebhookChanges(t *testing.T) {
	tests := []struct {
		name         string
		rootPath     string
		changedFiles []string
		want         bool
	}{
		{
			name:         "empty root path matches all",
			rootPath:     "",
			changedFiles: []string{"aws/dev/main.tf"},
			want:         true,
		},
		{
			name:         "root path prefix match",
			rootPath:     "aws/dev",
			changedFiles: []string{"aws/dev/envs/prod/main.tf"},
			want:         true,
		},
		{
			name:         "root path exact path match",
			rootPath:     "aws/dev",
			changedFiles: []string{"aws/dev"},
			want:         true,
		},
		{
			name:         "non matching root path",
			rootPath:     "aws/staging",
			changedFiles: []string{"aws/dev/envs/prod/main.tf"},
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectCfg := &config.ProjectConfig{RootPath: tc.rootPath}
			if got := projectPathMatchesWebhookChanges(projectCfg, tc.changedFiles); got != tc.want {
				t.Fatalf("projectPathMatchesWebhookChanges() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractChangedFilesDedupAndMaxFiles(t *testing.T) {
	payload := gitHubPushPayload{
		Commits: []struct {
			Added    []string `json:"added"`
			Modified []string `json:"modified"`
			Removed  []string `json:"removed"`
		}{
			{
				Added:    []string{"envs/prod/main.tf", "README.md"},
				Modified: []string{"envs/prod/main.tf", "envs/prod/vars.tfvars"},
			},
			{
				Removed: []string{"envs/prod/main.tf"},
			},
		},
	}

	got := extractChangedFiles(payload, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 files due to max_files limit, got %d (%v)", len(got), got)
	}
	if got[0] != "envs/prod/main.tf" || got[1] != "envs/prod/vars.tfvars" {
		t.Fatalf("unexpected changed files: %v", got)
	}
}

func TestWebhookDuplicateDeliveryIsIgnored(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, _, cleanup := newTestServerWithConfig(t, runner, []string{"envs/prod"}, false, nil, true, func(cfg *config.Config) {
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
			Name:          "project",
			DefaultBranch: "main",
			CloneURL:      srv.cfg.GetProject("project").URL,
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
	signature := "sha256=" + computeTestHMAC(body, "secret")

	firstReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/webhooks/github", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	firstReq.Header.Set("X-GitHub-Event", "push")
	firstReq.Header.Set("X-Hub-Signature-256", signature)
	firstReq.Header.Set("X-GitHub-Delivery", "delivery-1")
	firstResp, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from first delivery, got %d", firstResp.StatusCode)
	}

	secondReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/webhooks/github", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	secondReq.Header.Set("X-GitHub-Event", "push")
	secondReq.Header.Set("X-Hub-Signature-256", signature)
	secondReq.Header.Set("X-GitHub-Delivery", "delivery-1")
	secondResp, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 from duplicate delivery, got %d", secondResp.StatusCode)
	}
}
