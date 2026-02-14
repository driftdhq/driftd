package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/secrets"
)

const webhookReplayWindow = 15 * time.Minute

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
	if len(candidates) == 0 && isValidProjectName(payload.Repository.Name) {
		projectCfg, lookupErr := s.getProjectConfig(payload.Repository.Name)
		if lookupErr == nil && projectCfg != nil {
			candidates = append(candidates, projectCfg)
		} else if lookupErr != nil && lookupErr != secrets.ErrProjectNotFound {
			http.Error(w, s.sanitizeErrorMessage(lookupErr.Error()), http.StatusInternalServerError)
			return
		}
	}
	if len(candidates) == 0 {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Project not configured"})
		return
	}

	trigger := "webhook"
	var (
		apiScans            []*apiScan
		stackIDs            []string
		branchMatchedConfig bool
	)
	for _, projectCfg := range candidates {
		if !projectMatchesWebhookBranch(projectCfg, branch, payload.Repository.DefaultBranch) {
			continue
		}
		if !projectPathMatchesWebhookChanges(projectCfg, changedFiles) {
			continue
		}
		branchMatchedConfig = true

		scan, stacks, err := s.startScanWithCancel(r.Context(), projectCfg, trigger, payload.HeadCommit.ID, payload.Pusher.Name)
		if err != nil {
			if err == queue.ErrProjectLocked {
				continue
			}
			http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
			return
		}

		targetStacks := selectStacksForChanges(stacks, changedFiles)
		if len(targetStacks) == 0 {
			_ = s.queue.FailScan(r.Context(), scan.ID, projectCfg.Name, "no matching stacks for webhook changes")
			continue
		}

		enqResult, err := s.orchestrator.EnqueueStacks(r.Context(), scan, projectCfg, targetStacks, trigger, payload.HeadCommit.ID, payload.Pusher.Name)
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

func projectMatchesWebhookBranch(projectCfg *config.ProjectConfig, payloadBranch, payloadDefaultBranch string) bool {
	if projectCfg == nil {
		return false
	}
	target := projectCfg.Branch
	if target == "" {
		target = payloadDefaultBranch
	}
	if target == "" {
		return true
	}
	return payloadBranch == target
}

func projectPathMatchesWebhookChanges(projectCfg *config.ProjectConfig, changedFiles []string) bool {
	if projectCfg == nil {
		return false
	}
	rootPath := filepath.ToSlash(strings.Trim(strings.TrimSpace(projectCfg.RootPath), "/"))
	if rootPath == "" {
		return true
	}
	for _, file := range changedFiles {
		normalized := filepath.ToSlash(strings.Trim(strings.TrimSpace(file), "/"))
		if normalized == rootPath || strings.HasPrefix(normalized, rootPath+"/") {
			return true
		}
	}
	return false
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
		if !s.recordWebhookDelivery(r, body) {
			w.WriteHeader(http.StatusAccepted)
			return false
		}
		return true
	}

	if s.cfg.Webhook.Token != "" {
		token := r.Header.Get(s.cfg.Webhook.TokenHeader)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Webhook.Token)) != 1 {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return false
		}
		if !s.recordWebhookDelivery(r, body) {
			w.WriteHeader(http.StatusAccepted)
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

func (s *Server) recordWebhookDelivery(r *http.Request, body []byte) bool {
	key := webhookReplayKey(r, body)
	now := time.Now().UTC()

	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()

	cutoff := now.Add(-webhookReplayWindow)
	for k, seenAt := range s.webhookSeen {
		if seenAt.Before(cutoff) {
			delete(s.webhookSeen, k)
		}
	}

	if seenAt, ok := s.webhookSeen[key]; ok && now.Sub(seenAt) <= webhookReplayWindow {
		return false
	}

	s.webhookSeen[key] = now
	return true
}

func webhookReplayKey(r *http.Request, body []byte) string {
	if delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery")); delivery != "" {
		return "delivery:" + delivery
	}
	if sig := strings.TrimSpace(r.Header.Get("X-Hub-Signature-256")); sig != "" {
		return "sig:" + sig
	}
	sum := sha256.Sum256(body)
	return "body:" + hex.EncodeToString(sum[:])
}
