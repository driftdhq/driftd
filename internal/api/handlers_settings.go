package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/driftdhq/driftd/internal/secrets"
	"github.com/go-chi/chi/v5"
)

// RepoRequest is the JSON request body for creating/updating a repository.
type RepoRequest struct {
	Name                       string   `json:"name"`
	URL                        string   `json:"url"`
	Branch                     *string  `json:"branch,omitempty"`
	IgnorePaths                []string `json:"ignore_paths,omitempty"`
	Schedule                   *string  `json:"schedule,omitempty"`
	CancelInflightOnNewTrigger *bool    `json:"cancel_inflight_on_new_trigger,omitempty"`

	// Auth configuration
	AuthType string `json:"auth_type"` // "https", "ssh", "github_app"

	// GitHub App auth
	GitHubAppID          int64  `json:"github_app_id,omitempty"`
	GitHubInstallationID int64  `json:"github_installation_id,omitempty"`
	GitHubPrivateKey     string `json:"github_private_key,omitempty"`

	// SSH auth
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`

	// HTTPS auth
	HTTPSUsername string `json:"https_username,omitempty"`
	HTTPSToken    string `json:"https_token,omitempty"`
}

// RepoResponse is the JSON response for a repository.
type RepoResponse struct {
	Name                       string   `json:"name"`
	URL                        string   `json:"url"`
	Branch                     string   `json:"branch,omitempty"`
	IgnorePaths                []string `json:"ignore_paths,omitempty"`
	Schedule                   string   `json:"schedule,omitempty"`
	CancelInflightOnNewTrigger bool     `json:"cancel_inflight_on_new_trigger"`

	AuthType             string `json:"auth_type"`
	GitHubAppID          int64  `json:"github_app_id,omitempty"`
	GitHubInstallationID int64  `json:"github_installation_id,omitempty"`

	// Source indicates where the repo config comes from
	Source    string `json:"source"` // "config" or "dynamic"
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// handleListSettingsRepos returns all configured repositories.
func (s *Server) handleListSettingsRepos(w http.ResponseWriter, r *http.Request) {
	repos := make([]RepoResponse, 0)

	// Add static repos from config
	for _, repo := range s.cfg.Repos {
		resp := RepoResponse{
			Name:                       repo.Name,
			URL:                        repo.URL,
			Branch:                     repo.Branch,
			IgnorePaths:                repo.IgnorePaths,
			Schedule:                   repo.Schedule,
			CancelInflightOnNewTrigger: repo.CancelInflightEnabled(),
			Source:                     "config",
		}
		if repo.Git != nil {
			resp.AuthType = repo.Git.Type
			if repo.Git.GitHubApp != nil {
				resp.GitHubAppID = repo.Git.GitHubApp.AppID
				resp.GitHubInstallationID = repo.Git.GitHubApp.InstallationID
			}
		}
		repos = append(repos, resp)
	}

	// Add dynamic repos from repo store
	if s.repoStore != nil {
		dynamicRepos := s.repoStore.List()
		for _, repo := range dynamicRepos {
			// Skip if already in static config (static takes precedence for display)
			if s.cfg.GetRepo(repo.Name) != nil {
				continue
			}

			resp := RepoResponse{
				Name:                       repo.Name,
				URL:                        repo.URL,
				Branch:                     repo.Branch,
				IgnorePaths:                repo.IgnorePaths,
				Schedule:                   repo.Schedule,
				CancelInflightOnNewTrigger: repo.CancelInflightOnNewTrigger,
				AuthType:                   repo.Git.Type,
				Source:                     "dynamic",
				CreatedAt:                  repo.CreatedAt.Format("2006-01-02T15:04:05Z"),
				UpdatedAt:                  repo.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if repo.Git.GitHubApp != nil {
				resp.GitHubAppID = repo.Git.GitHubApp.AppID
				resp.GitHubInstallationID = repo.Git.GitHubApp.InstallationID
			}
			repos = append(repos, resp)
		}
	}

	writeJSON(w, http.StatusOK, repos)
}

// handleGetSettingsRepo returns a single repository by name.
func (s *Server) handleGetSettingsRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	// Check static config first
	if repo := s.cfg.GetRepo(repoName); repo != nil {
		resp := RepoResponse{
			Name:                       repo.Name,
			URL:                        repo.URL,
			Branch:                     repo.Branch,
			IgnorePaths:                repo.IgnorePaths,
			Schedule:                   repo.Schedule,
			CancelInflightOnNewTrigger: repo.CancelInflightEnabled(),
			Source:                     "config",
		}
		if repo.Git != nil {
			resp.AuthType = repo.Git.Type
			if repo.Git.GitHubApp != nil {
				resp.GitHubAppID = repo.Git.GitHubApp.AppID
				resp.GitHubInstallationID = repo.Git.GitHubApp.InstallationID
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Check dynamic repos
	if s.repoStore != nil {
		repo, err := s.repoStore.Get(repoName)
		if err == nil {
			resp := RepoResponse{
				Name:                       repo.Name,
				URL:                        repo.URL,
				Branch:                     repo.Branch,
				IgnorePaths:                repo.IgnorePaths,
				Schedule:                   repo.Schedule,
				CancelInflightOnNewTrigger: repo.CancelInflightOnNewTrigger,
				AuthType:                   repo.Git.Type,
				Source:                     "dynamic",
				CreatedAt:                  repo.CreatedAt.Format("2006-01-02T15:04:05Z"),
				UpdatedAt:                  repo.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if repo.Git.GitHubApp != nil {
				resp.GitHubAppID = repo.Git.GitHubApp.AppID
				resp.GitHubInstallationID = repo.Git.GitHubApp.InstallationID
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
}

// handleCreateSettingsRepo creates a new repository configuration.
func (s *Server) handleCreateSettingsRepo(w http.ResponseWriter, r *http.Request) {
	if s.repoStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic repository management not enabled",
		})
		return
	}

	var req RepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// Validate required fields
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}
	if req.AuthType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "auth_type is required"})
		return
	}

	// Validate name format (alphanumeric, hyphens, underscores only)
	if !isValidRepoName(req.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "name must contain only alphanumeric characters, hyphens, and underscores",
		})
		return
	}

	// Check for conflicts with static config
	if s.cfg.GetRepo(req.Name) != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "repository name conflicts with static configuration",
		})
		return
	}

	// Build entry and credentials
	entry := &secrets.RepoEntry{
		Name:                       req.Name,
		URL:                        req.URL,
		Branch:                     derefString(req.Branch),
		IgnorePaths:                req.IgnorePaths,
		Schedule:                   derefString(req.Schedule),
		CancelInflightOnNewTrigger: derefBool(req.CancelInflightOnNewTrigger, true),
		Git: secrets.RepoGitConfig{
			Type: req.AuthType,
		},
	}

	creds := &secrets.RepoCredentials{}

	switch req.AuthType {
	case "github_app":
		if req.GitHubAppID == 0 || req.GitHubInstallationID == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "github_app_id and github_installation_id are required for github_app auth",
			})
			return
		}
		if req.GitHubPrivateKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "github_private_key is required for github_app auth",
			})
			return
		}
		entry.Git.GitHubApp = &secrets.RepoGitHubApp{
			AppID:          req.GitHubAppID,
			InstallationID: req.GitHubInstallationID,
		}
		creds.GitHubAppPrivateKey = req.GitHubPrivateKey

	case "ssh":
		if req.SSHPrivateKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "ssh_private_key is required for ssh auth",
			})
			return
		}
		if req.SSHKnownHosts == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "ssh_known_hosts is required for ssh auth",
			})
			return
		}
		creds.SSHPrivateKey = req.SSHPrivateKey
		creds.SSHKnownHosts = req.SSHKnownHosts

	case "https":
		if req.HTTPSToken != "" {
			creds.HTTPSToken = req.HTTPSToken
			creds.HTTPSUsername = req.HTTPSUsername
		}

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "auth_type must be one of: github_app, ssh, https",
		})
		return
	}

	// Add to store
	if err := s.repoStore.Add(entry, creds); err != nil {
		if errors.Is(err, secrets.ErrRepoAlreadyExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "repository already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Register with scheduler if schedule is set
	if entry.Schedule != "" && s.onRepoAdded != nil {
		s.onRepoAdded(req.Name, entry.Schedule)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

// handleUpdateSettingsRepo updates an existing repository configuration.
func (s *Server) handleUpdateSettingsRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	// Check if it's a static repo (can't be modified)
	if s.cfg.GetRepo(repoName) != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cannot modify repository defined in static configuration",
		})
		return
	}

	if s.repoStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic repository management not enabled",
		})
		return
	}

	// Check if repo exists
	existing, err := s.repoStore.Get(repoName)
	if err != nil {
		if errors.Is(err, secrets.ErrRepoNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var req RepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Name != "" && req.Name != existing.Name {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "renaming repositories is not supported"})
		return
	}
	if req.URL == "" {
		req.URL = existing.URL
	}
	if req.AuthType == "" {
		req.AuthType = existing.Git.Type
	}

	// Build updated entry
	entry := &secrets.RepoEntry{
		Name:                       existing.Name,
		URL:                        req.URL,
		Branch:                     existing.Branch,
		IgnorePaths:                existing.IgnorePaths,
		Schedule:                   existing.Schedule,
		CancelInflightOnNewTrigger: existing.CancelInflightOnNewTrigger,
		Git: secrets.RepoGitConfig{
			Type: req.AuthType,
		},
	}
	if req.Branch != nil {
		entry.Branch = *req.Branch
	}
	if req.IgnorePaths != nil {
		entry.IgnorePaths = req.IgnorePaths
	}
	if req.Schedule != nil {
		entry.Schedule = *req.Schedule
	}
	if req.CancelInflightOnNewTrigger != nil {
		entry.CancelInflightOnNewTrigger = *req.CancelInflightOnNewTrigger
	}

	authChanged := req.AuthType != "" && req.AuthType != existing.Git.Type

	// Only update credentials if provided
	var creds *secrets.RepoCredentials
	if req.GitHubPrivateKey != "" || req.SSHPrivateKey != "" || req.HTTPSToken != "" {
		creds = &secrets.RepoCredentials{}
		switch req.AuthType {
		case "github_app":
			if req.GitHubAppID == 0 || req.GitHubInstallationID == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "github_app_id and github_installation_id are required for github_app auth",
				})
				return
			}
			entry.Git.GitHubApp = &secrets.RepoGitHubApp{
				AppID:          req.GitHubAppID,
				InstallationID: req.GitHubInstallationID,
			}
			creds.GitHubAppPrivateKey = req.GitHubPrivateKey
		case "ssh":
			if req.SSHKnownHosts == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "ssh_known_hosts is required for ssh auth",
				})
				return
			}
			creds.SSHPrivateKey = req.SSHPrivateKey
			creds.SSHKnownHosts = req.SSHKnownHosts
		case "https":
			creds.HTTPSToken = req.HTTPSToken
			creds.HTTPSUsername = req.HTTPSUsername
		}
	} else if req.AuthType == "github_app" {
		// Preserve existing GitHub App IDs if not updating creds
		entry.Git.GitHubApp = existing.Git.GitHubApp
		if req.GitHubAppID != 0 {
			if entry.Git.GitHubApp == nil {
				entry.Git.GitHubApp = &secrets.RepoGitHubApp{}
			}
			entry.Git.GitHubApp.AppID = req.GitHubAppID
		}
		if req.GitHubInstallationID != 0 {
			if entry.Git.GitHubApp == nil {
				entry.Git.GitHubApp = &secrets.RepoGitHubApp{}
			}
			entry.Git.GitHubApp.InstallationID = req.GitHubInstallationID
		}
	}
	if authChanged && creds == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "credentials are required when changing auth_type",
		})
		return
	}

	if err := s.repoStore.Update(repoName, entry, creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Update scheduler if schedule changed
	if s.onRepoUpdated != nil {
		s.onRepoUpdated(entry.Name, entry.Schedule)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleDeleteSettingsRepo deletes a repository configuration.
func (s *Server) handleDeleteSettingsRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	// Check if it's a static repo (can't be deleted)
	if s.cfg.GetRepo(repoName) != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cannot delete repository defined in static configuration",
		})
		return
	}

	if s.repoStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic repository management not enabled",
		})
		return
	}

	if err := s.repoStore.Delete(repoName); err != nil {
		if errors.Is(err, secrets.ErrRepoNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Remove from scheduler
	if s.onRepoDeleted != nil {
		s.onRepoDeleted(repoName)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestRepoConnection tests the connection to a repository.
func (s *Server) handleTestRepoConnection(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement connection test
	// This would attempt to git ls-remote with the provided credentials
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented yet",
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefBool(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}
