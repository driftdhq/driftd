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

func TestWebhookMonorepoPrefiltersByRootPath(t *testing.T) {
	runner := &fakeRunner{}
	srv, ts, q, cleanup := newTestServerWithConfig(t, runner, []string{"aws/dev/envs/prod", "aws/staging/envs/prod"}, false, nil, true, func(cfg *config.Config) {
		cfg.Webhook.Enabled = true
		cfg.Webhook.GitHubSecret = "secret"
		baseURL := cfg.Repos[0].URL
		cancelInflight := true
		cfg.Repos = []config.RepoConfig{
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
			CloneURL:      srv.cfg.GetRepo("aws-dev").URL,
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
	if sr.Scans[0].RepoName != "aws-dev" {
		t.Fatalf("expected aws-dev scan, got %s", sr.Scans[0].RepoName)
	}

	if _, err := q.GetActiveScan(context.Background(), "aws-dev"); err != nil {
		t.Fatalf("expected active scan for aws-dev: %v", err)
	}
	if _, err := q.GetActiveScan(context.Background(), "aws-staging"); err != queue.ErrScanNotFound {
		t.Fatalf("expected no scan for aws-staging, got %v", err)
	}
	stagingStacks, err := q.ListRepoStackScans(context.Background(), "aws-staging", 10)
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
		cfg.Repos[0].Name = "github-repo"
		cfg.Repos[0].URL = "https://github.com/example/infra.git"
		cfg.Repos[0].CloneURL = cfg.Repos[0].URL
	})
	defer cleanup()

	sshMatches, err := srv.getReposByURL("", "git@github.com:example/infra.git", "")
	if err != nil {
		t.Fatalf("ssh lookup failed: %v", err)
	}
	if len(sshMatches) != 1 || sshMatches[0].Name != "github-repo" {
		t.Fatalf("expected github-repo for ssh lookup, got %#v", sshMatches)
	}

	htmlMatches, err := srv.getReposByURL("", "", "https://github.com/example/infra")
	if err != nil {
		t.Fatalf("html lookup failed: %v", err)
	}
	if len(htmlMatches) != 1 || htmlMatches[0].Name != "github-repo" {
		t.Fatalf("expected github-repo for html lookup, got %#v", htmlMatches)
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
		cfg.Repos[0].Branch = "release"
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
	if _, err := q.GetActiveScan(context.Background(), "repo"); err != queue.ErrScanNotFound {
		t.Fatalf("expected no active scan on branch mismatch")
	}
}

func TestRepoMatchesWebhookBranch(t *testing.T) {
	tests := []struct {
		name                 string
		repoBranch           string
		payloadBranch        string
		payloadDefaultBranch string
		want                 bool
	}{
		{
			name:                 "uses configured branch",
			repoBranch:           "release",
			payloadBranch:        "release",
			payloadDefaultBranch: "main",
			want:                 true,
		},
		{
			name:                 "configured branch mismatch",
			repoBranch:           "release",
			payloadBranch:        "main",
			payloadDefaultBranch: "main",
			want:                 false,
		},
		{
			name:                 "falls back to payload default branch",
			repoBranch:           "",
			payloadBranch:        "main",
			payloadDefaultBranch: "main",
			want:                 true,
		},
		{
			name:                 "no configured or default branch accepts",
			repoBranch:           "",
			payloadBranch:        "feature/foo",
			payloadDefaultBranch: "",
			want:                 true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoCfg := &config.RepoConfig{Branch: tc.repoBranch}
			if got := repoMatchesWebhookBranch(repoCfg, tc.payloadBranch, tc.payloadDefaultBranch); got != tc.want {
				t.Fatalf("repoMatchesWebhookBranch() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRepoPathMatchesWebhookChanges(t *testing.T) {
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
			repoCfg := &config.RepoConfig{RootPath: tc.rootPath}
			if got := repoPathMatchesWebhookChanges(repoCfg, tc.changedFiles); got != tc.want {
				t.Fatalf("repoPathMatchesWebhookChanges() = %v, want %v", got, tc.want)
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
