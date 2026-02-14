package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/secrets"
)

type gitHubPushPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		CloneURL      string `json:"clone_url"`
		SSHURL        string `json:"ssh_url"`
		HTMLURL       string `json:"html_url"`
	} `json:"repository"`
	HeadCommit struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
	Commits []struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	if !s.validateWebhookRequest(w, r, body) {
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "push" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var payload gitHubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(payload.Ref, "refs/heads/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")

	changedFiles := extractChangedFiles(payload, s.cfg.Webhook.MaxFiles)
	if len(changedFiles) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	candidates, err := s.getReposByURL(payload.Repository.CloneURL, payload.Repository.SSHURL, payload.Repository.HTMLURL)
	if err != nil {
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}
	if len(candidates) == 0 && isValidRepoName(payload.Repository.Name) {
		repoCfg, lookupErr := s.getRepoConfig(payload.Repository.Name)
		if lookupErr == nil && repoCfg != nil {
			candidates = append(candidates, repoCfg)
		} else if lookupErr != nil && lookupErr != secrets.ErrRepoNotFound {
			http.Error(w, s.sanitizeErrorMessage(lookupErr.Error()), http.StatusInternalServerError)
			return
		}
	}
	if len(candidates) == 0 {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Repository not configured"})
		return
	}

	trigger := "webhook"
	var (
		apiScans            []*apiScan
		stackIDs            []string
		branchMatchedConfig bool
	)
	for _, repoCfg := range candidates {
		if !repoMatchesWebhookBranch(repoCfg, branch, payload.Repository.DefaultBranch) {
			continue
		}
		branchMatchedConfig = true

		scan, stacks, err := s.startScanWithCancel(r.Context(), repoCfg, trigger, payload.HeadCommit.ID, payload.Pusher.Name)
		if err != nil {
			if err == queue.ErrRepoLocked {
				continue
			}
			http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
			return
		}

		targetStacks := selectStacksForChanges(stacks, changedFiles)
		if len(targetStacks) == 0 {
			_ = s.queue.FailScan(r.Context(), scan.ID, repoCfg.Name, "no matching stacks for webhook changes")
			continue
		}

		enqResult, err := s.orchestrator.EnqueueStacks(r.Context(), scan, repoCfg, targetStacks, trigger, payload.HeadCommit.ID, payload.Pusher.Name)
		if err != nil && err != orchestrate.ErrNoStacksEnqueued {
			http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
			return
		}

		apiScans = append(apiScans, toAPIScan(scan))
		if enqResult != nil {
			stackIDs = append(stackIDs, enqResult.StackIDs...)
		}
	}

	if !branchMatchedConfig || len(apiScans) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := scanResponse{
		Stacks:  stackIDs,
		Scans:   apiScans,
		Message: fmt.Sprintf("Enqueued %d stacks", len(stackIDs)),
	}
	if len(apiScans) == 1 {
		resp.Scan = apiScans[0]
	}
	json.NewEncoder(w).Encode(resp)
}

func extractChangedFiles(payload gitHubPushPayload, maxFiles int) []string {
	seen := map[string]struct{}{}
	var files []string
	for _, commit := range payload.Commits {
		for _, path := range append(append(commit.Added, commit.Modified...), commit.Removed...) {
			path = strings.TrimPrefix(path, "/")
			if path == "" {
				continue
			}
			if !isInfraFile(path) {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			files = append(files, filepath.ToSlash(path))
			if maxFiles > 0 && len(files) >= maxFiles {
				return files
			}
		}
	}
	return files
}

func selectStacksForChanges(stacks []string, changedFiles []string) []string {
	if len(changedFiles) == 0 {
		return nil
	}
	stackSet := map[string]struct{}{}
	for _, stack := range stacks {
		stackSet[stack] = struct{}{}
	}
	selected := map[string]struct{}{}
	for _, file := range changedFiles {
		for stack := range stackSet {
			if stack != "" && !strings.HasPrefix(file, stack+"/") {
				continue
			}
			selected[stack] = struct{}{}
		}
	}
	var result []string
	for stack := range selected {
		result = append(result, stack)
	}
	sort.Strings(result)
	return result
}

func isInfraFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".tf") || strings.HasSuffix(base, ".tfvars") || base == "terragrunt.hcl" {
		return true
	}
	if strings.HasSuffix(base, ".hcl") {
		return true
	}
	if strings.HasSuffix(path, ".tf.json") || strings.HasSuffix(path, ".tfvars.json") {
		return true
	}
	return false
}

func repoMatchesWebhookBranch(repoCfg *config.RepoConfig, payloadBranch, payloadDefaultBranch string) bool {
	if repoCfg == nil {
		return false
	}
	target := repoCfg.Branch
	if target == "" {
		target = payloadDefaultBranch
	}
	if target == "" {
		return true
	}
	return payloadBranch == target
}

func (s *Server) validateWebhookRequest(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if s.cfg.Webhook.GitHubSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			http.Error(w, "Missing signature", http.StatusUnauthorized)
			return false
		}
		parts := strings.Split(sig, "=")
		if len(parts) != 2 || parts[0] != "sha256" {
			http.Error(w, "Invalid signature format", http.StatusUnauthorized)
			return false
		}
		expected := computeHMACSHA256(body, []byte(s.cfg.Webhook.GitHubSecret))
		provided, err := hex.DecodeString(parts[1])
		if err != nil || !hmac.Equal(expected, provided) {
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return false
		}
		return true
	}

	if s.cfg.Webhook.Token != "" {
		token := r.Header.Get(s.cfg.Webhook.TokenHeader)
		if token == "" || token != s.cfg.Webhook.Token {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return false
		}
		return true
	}

	http.Error(w, "Webhook not configured", http.StatusUnauthorized)
	return false
}

func computeHMACSHA256(payload, key []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(payload)
	return h.Sum(nil)
}
