package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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

	AuthType string `json:"auth_type"` // "https", "ssh", "github_app"
	IntegrationID string `json:"integration_id,omitempty"`

	GitHubAppID          int64  `json:"github_app_id,omitempty"`
	GitHubInstallationID int64  `json:"github_installation_id,omitempty"`
	GitHubPrivateKey     string `json:"github_private_key,omitempty"`

	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`

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
	IntegrationID        string `json:"integration_id,omitempty"`
	IntegrationName      string `json:"integration_name,omitempty"`
	IntegrationType      string `json:"integration_type,omitempty"`

	Source    string `json:"source"` // "config" or "dynamic"
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// IntegrationRequest is the JSON request body for creating/updating an integration.
type IntegrationRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`

	GitHubAppID          int64  `json:"github_app_id,omitempty"`
	GitHubInstallationID int64  `json:"github_installation_id,omitempty"`
	GitHubPrivateKeyPath string `json:"github_private_key_path,omitempty"`
	GitHubPrivateKeyEnv  string `json:"github_private_key_env,omitempty"`
	GitHubAPIBaseURL     string `json:"github_api_base_url,omitempty"`

	SSHKeyPath               string `json:"ssh_key_path,omitempty"`
	SSHKeyEnv                string `json:"ssh_key_env,omitempty"`
	SSHKeyPassphraseEnv      string `json:"ssh_key_passphrase_env,omitempty"`
	SSHKnownHostsPath        string `json:"ssh_known_hosts_path,omitempty"`
	SSHInsecureIgnoreHostKey bool   `json:"ssh_insecure_ignore_host_key,omitempty"`

	HTTPSUsername string `json:"https_username,omitempty"`
	HTTPSTokenEnv string `json:"https_token_env,omitempty"`
}

// IntegrationResponse is the JSON response for an integration.
type IntegrationResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`

	GitHubAppID          int64  `json:"github_app_id,omitempty"`
	GitHubInstallationID int64  `json:"github_installation_id,omitempty"`
	GitHubPrivateKeyPath string `json:"github_private_key_path,omitempty"`
	GitHubPrivateKeyEnv  string `json:"github_private_key_env,omitempty"`
	GitHubAPIBaseURL     string `json:"github_api_base_url,omitempty"`

	SSHKeyPath               string `json:"ssh_key_path,omitempty"`
	SSHKeyEnv                string `json:"ssh_key_env,omitempty"`
	SSHKeyPassphraseEnv      string `json:"ssh_key_passphrase_env,omitempty"`
	SSHKnownHostsPath        string `json:"ssh_known_hosts_path,omitempty"`
	SSHInsecureIgnoreHostKey bool   `json:"ssh_insecure_ignore_host_key,omitempty"`

	HTTPSUsername string `json:"https_username,omitempty"`
	HTTPSTokenEnv string `json:"https_token_env,omitempty"`

	Source    string `json:"source"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// handleListSettingsRepos returns all configured repositories.
func (s *Server) handleListSettingsRepos(w http.ResponseWriter, r *http.Request) {
	repos := make([]RepoResponse, 0)

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
			resp.IntegrationType = repo.Git.Type
		}
		repos = append(repos, resp)
	}

	if s.repoStore != nil {
		dynamicRepos := s.repoStore.List()
		for _, repo := range dynamicRepos {
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
				IntegrationID:              repo.IntegrationID,
				Source:                     "dynamic",
				CreatedAt:                  repo.CreatedAt.Format("2006-01-02T15:04:05Z"),
				UpdatedAt:                  repo.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if repo.Git.GitHubApp != nil {
				resp.GitHubAppID = repo.Git.GitHubApp.AppID
				resp.GitHubInstallationID = repo.Git.GitHubApp.InstallationID
			}
			if repo.IntegrationID != "" {
				if name, typ, ok := s.lookupIntegrationMeta(repo.IntegrationID); ok {
					resp.IntegrationName = name
					resp.IntegrationType = typ
				}
			} else if repo.Git.Type != "" {
				resp.IntegrationType = repo.Git.Type
			}
			repos = append(repos, resp)
		}
	}

	writeJSON(w, http.StatusOK, repos)
}

// handleGetSettingsRepo returns a single repository by name.
func (s *Server) handleGetSettingsRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

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
			resp.IntegrationType = repo.Git.Type
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

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
				IntegrationID:              repo.IntegrationID,
				Source:                     "dynamic",
				CreatedAt:                  repo.CreatedAt.Format("2006-01-02T15:04:05Z"),
				UpdatedAt:                  repo.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if repo.Git.GitHubApp != nil {
				resp.GitHubAppID = repo.Git.GitHubApp.AppID
				resp.GitHubInstallationID = repo.Git.GitHubApp.InstallationID
			}
			if repo.IntegrationID != "" {
				if name, typ, ok := s.lookupIntegrationMeta(repo.IntegrationID); ok {
					resp.IntegrationName = name
					resp.IntegrationType = typ
				}
			} else if repo.Git.Type != "" {
				resp.IntegrationType = repo.Git.Type
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

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}
	if req.AuthType == "" && req.IntegrationID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "integration_id is required"})
		return
	}

	if !isValidRepoName(req.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "name must contain only alphanumeric characters, hyphens, and underscores",
		})
		return
	}

	if s.cfg.GetRepo(req.Name) != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "repository name conflicts with static configuration",
		})
		return
	}

	entry := &secrets.RepoEntry{
		Name:                       req.Name,
		URL:                        req.URL,
		Branch:                     derefString(req.Branch),
		IgnorePaths:                req.IgnorePaths,
		Schedule:                   derefString(req.Schedule),
		CancelInflightOnNewTrigger: derefBool(req.CancelInflightOnNewTrigger, true),
		Git:                        secrets.RepoGitConfig{},
	}

	var creds *secrets.RepoCredentials

	if req.IntegrationID != "" {
		integration, err := s.getIntegration(req.IntegrationID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "integration_id not found",
			})
			return
		}
		entry.IntegrationID = integration.ID
	} else {
		creds = &secrets.RepoCredentials{}
		entry.Git.Type = req.AuthType
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
	}

	if err := s.repoStore.Add(entry, creds); err != nil {
		if errors.Is(err, secrets.ErrRepoAlreadyExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "repository already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if entry.Schedule != "" && s.onRepoAdded != nil {
		s.onRepoAdded(req.Name, entry.Schedule)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

// handleUpdateSettingsRepo updates an existing repository configuration.
func (s *Server) handleUpdateSettingsRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

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
	if req.AuthType == "" && req.IntegrationID == "" {
		req.AuthType = existing.Git.Type
	}
	if req.IntegrationID == "" {
		req.IntegrationID = existing.IntegrationID
	}

	entry := &secrets.RepoEntry{
		Name:                       existing.Name,
		URL:                        req.URL,
		Branch:                     existing.Branch,
		IgnorePaths:                existing.IgnorePaths,
		Schedule:                   existing.Schedule,
		CancelInflightOnNewTrigger: existing.CancelInflightOnNewTrigger,
		IntegrationID:              req.IntegrationID,
		Git:                        secrets.RepoGitConfig{Type: req.AuthType},
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
	integrationChanged := req.IntegrationID != "" && req.IntegrationID != existing.IntegrationID

	var creds *secrets.RepoCredentials
	if req.IntegrationID != "" {
		if _, err := s.getIntegration(req.IntegrationID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "integration_id not found",
			})
			return
		}
		entry.Git = secrets.RepoGitConfig{}
		authChanged = false
		creds = &secrets.RepoCredentials{}
	} else if req.GitHubPrivateKey != "" || req.SSHPrivateKey != "" || req.HTTPSToken != "" {
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
	if (authChanged || integrationChanged) && req.IntegrationID == "" && creds == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "credentials are required when changing auth_type",
		})
		return
	}

	if err := s.repoStore.Update(repoName, entry, creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if s.onRepoUpdated != nil {
		s.onRepoUpdated(entry.Name, entry.Schedule)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleDeleteSettingsRepo deletes a repository configuration.
func (s *Server) handleDeleteSettingsRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

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

	if s.onRepoDeleted != nil {
		s.onRepoDeleted(repoName)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestRepoConnection tests the connection to a repository.
func (s *Server) handleTestRepoConnection(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented yet",
	})
}

// handleListSettingsIntegrations returns all configured integrations.
func (s *Server) handleListSettingsIntegrations(w http.ResponseWriter, r *http.Request) {
	if s.intStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic integration management not enabled",
		})
		return
	}

	entries := s.intStore.List()
	responses := make([]IntegrationResponse, 0, len(entries))
	for _, entry := range entries {
		responses = append(responses, integrationResponseFromEntry(entry))
	}
	writeJSON(w, http.StatusOK, responses)
}

// handleGetSettingsIntegration returns a single integration by ID.
func (s *Server) handleGetSettingsIntegration(w http.ResponseWriter, r *http.Request) {
	if s.intStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic integration management not enabled",
		})
		return
	}

	id := chi.URLParam(r, "integration")
	entry, err := s.intStore.Get(id)
	if err != nil {
		if errors.Is(err, secrets.ErrIntegrationNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "integration not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, integrationResponseFromEntry(entry))
}

// handleCreateSettingsIntegration creates a new integration.
func (s *Server) handleCreateSettingsIntegration(w http.ResponseWriter, r *http.Request) {
	if s.intStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic integration management not enabled",
		})
		return
	}

	var req IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type is required"})
		return
	}

	entry, err := integrationEntryFromRequest("", req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	entry.ID = newIntegrationID(req.Name)

	if err := s.intStore.Add(entry); err != nil {
		if errors.Is(err, secrets.ErrIntegrationAlreadyExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "integration already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, integrationResponseFromEntry(entry))
}

// handleUpdateSettingsIntegration updates an integration.
func (s *Server) handleUpdateSettingsIntegration(w http.ResponseWriter, r *http.Request) {
	if s.intStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic integration management not enabled",
		})
		return
	}

	id := chi.URLParam(r, "integration")
	existing, err := s.intStore.Get(id)
	if err != nil {
		if errors.Is(err, secrets.ErrIntegrationNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "integration not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var req IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Name == "" {
		req.Name = existing.Name
	}
	if req.Type == "" {
		req.Type = existing.Type
	}

	entry, err := integrationEntryFromRequest(id, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := s.intStore.Update(id, entry); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, integrationResponseFromEntry(entry))
}

// handleDeleteSettingsIntegration deletes an integration.
func (s *Server) handleDeleteSettingsIntegration(w http.ResponseWriter, r *http.Request) {
	if s.intStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic integration management not enabled",
		})
		return
	}

	id := chi.URLParam(r, "integration")
	if s.repoStore != nil {
		for _, repo := range s.repoStore.List() {
			if repo.IntegrationID == id {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "integration is still referenced by repositories",
				})
				return
			}
		}
	}

	if err := s.intStore.Delete(id); err != nil {
		if errors.Is(err, secrets.ErrIntegrationNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "integration not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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

func (s *Server) lookupIntegrationMeta(id string) (string, string, bool) {
	if s.intStore == nil || id == "" {
		return "", "", false
	}
	entry, err := s.intStore.Get(id)
	if err != nil {
		return "", "", false
	}
	return entry.Name, entry.Type, true
}

func (s *Server) getIntegration(id string) (*secrets.IntegrationEntry, error) {
	if s.intStore == nil {
		return nil, fmt.Errorf("integration store not configured")
	}
	return s.intStore.Get(id)
}

func newIntegrationID(name string) string {
	base := strings.ToLower(name)
	base = strings.ReplaceAll(base, " ", "-")
	base = strings.ReplaceAll(base, "_", "-")
	base = strings.ReplaceAll(base, ".", "-")
	base = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, base)
	if base == "" {
		base = "integration"
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}

func integrationEntryFromRequest(id string, req IntegrationRequest) (*secrets.IntegrationEntry, error) {
	entry := &secrets.IntegrationEntry{
		ID:   id,
		Name: req.Name,
		Type: req.Type,
	}

	switch req.Type {
	case "github_app":
		if req.GitHubAppID == 0 || req.GitHubInstallationID == 0 {
			return nil, fmt.Errorf("github_app_id and github_installation_id are required")
		}
		if req.GitHubPrivateKeyPath == "" && req.GitHubPrivateKeyEnv == "" {
			return nil, fmt.Errorf("github_private_key_path or github_private_key_env is required")
		}
		entry.GitHubApp = &secrets.IntegrationGitHubApp{
			AppID:          req.GitHubAppID,
			InstallationID: req.GitHubInstallationID,
			PrivateKeyPath: req.GitHubPrivateKeyPath,
			PrivateKeyEnv:  req.GitHubPrivateKeyEnv,
			APIBaseURL:     req.GitHubAPIBaseURL,
		}
	case "ssh":
		if req.SSHKeyPath == "" && req.SSHKeyEnv == "" {
			return nil, fmt.Errorf("ssh_key_path or ssh_key_env is required")
		}
		if req.SSHKnownHostsPath == "" && !req.SSHInsecureIgnoreHostKey {
			return nil, fmt.Errorf("ssh_known_hosts_path is required unless ssh_insecure_ignore_host_key is true")
		}
		entry.SSH = &secrets.IntegrationSSH{
			KeyPath:               req.SSHKeyPath,
			KeyEnv:                req.SSHKeyEnv,
			KeyPassphraseEnv:      req.SSHKeyPassphraseEnv,
			KnownHostsPath:        req.SSHKnownHostsPath,
			InsecureIgnoreHostKey: req.SSHInsecureIgnoreHostKey,
		}
	case "https":
		if req.HTTPSTokenEnv == "" {
			return nil, fmt.Errorf("https_token_env is required")
		}
		entry.HTTPS = &secrets.IntegrationHTTPS{
			Username: req.HTTPSUsername,
			TokenEnv: req.HTTPSTokenEnv,
		}
	default:
		return nil, fmt.Errorf("type must be one of: github_app, ssh, https")
	}

	return entry, nil
}

func integrationResponseFromEntry(entry *secrets.IntegrationEntry) IntegrationResponse {
	resp := IntegrationResponse{
		ID:        entry.ID,
		Name:      entry.Name,
		Type:      entry.Type,
		Source:    "dynamic",
		CreatedAt: entry.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: entry.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if entry.GitHubApp != nil {
		resp.GitHubAppID = entry.GitHubApp.AppID
		resp.GitHubInstallationID = entry.GitHubApp.InstallationID
		resp.GitHubPrivateKeyPath = entry.GitHubApp.PrivateKeyPath
		resp.GitHubPrivateKeyEnv = entry.GitHubApp.PrivateKeyEnv
		resp.GitHubAPIBaseURL = entry.GitHubApp.APIBaseURL
	}
	if entry.SSH != nil {
		resp.SSHKeyPath = entry.SSH.KeyPath
		resp.SSHKeyEnv = entry.SSH.KeyEnv
		resp.SSHKeyPassphraseEnv = entry.SSH.KeyPassphraseEnv
		resp.SSHKnownHostsPath = entry.SSH.KnownHostsPath
		resp.SSHInsecureIgnoreHostKey = entry.SSH.InsecureIgnoreHostKey
	}
	if entry.HTTPS != nil {
		resp.HTTPSUsername = entry.HTTPS.Username
		resp.HTTPSTokenEnv = entry.HTTPS.TokenEnv
	}
	return resp
}
