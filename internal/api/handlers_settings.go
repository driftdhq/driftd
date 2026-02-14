package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/secrets"
	"github.com/go-chi/chi/v5"
	git "github.com/go-git/go-git/v5"
	gitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
)

// ProjectRequest is the JSON request body for creating/updating a project.
type ProjectRequest struct {
	Name                       string   `json:"name"`
	URL                        string   `json:"url"`
	Branch                     *string  `json:"branch,omitempty"`
	IgnorePaths                []string `json:"ignore_paths,omitempty"`
	Schedule                   *string  `json:"schedule,omitempty"`
	CancelInflightOnNewTrigger *bool    `json:"cancel_inflight_on_new_trigger,omitempty"`

	AuthType      string  `json:"auth_type"` // "https", "ssh", "github_app"
	IntegrationID *string `json:"integration_id,omitempty"`

	GitHubAppID          int64  `json:"github_app_id,omitempty"`
	GitHubInstallationID int64  `json:"github_installation_id,omitempty"`
	GitHubPrivateKey     string `json:"github_private_key,omitempty"`

	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`

	HTTPSUsername string `json:"https_username,omitempty"`
	HTTPSToken    string `json:"https_token,omitempty"`
}

// ProjectResponse is the JSON response for a project.
type ProjectResponse struct {
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
	projects := make([]ProjectResponse, 0)

	for _, project := range s.cfg.Projects {
		resp := ProjectResponse{
			Name:                       project.Name,
			URL:                        project.URL,
			Branch:                     project.Branch,
			IgnorePaths:                project.IgnorePaths,
			Schedule:                   project.Schedule,
			CancelInflightOnNewTrigger: project.CancelInflightEnabled(),
			Source:                     "config",
		}
		if project.Git != nil {
			resp.AuthType = project.Git.Type
			if project.Git.GitHubApp != nil {
				resp.GitHubAppID = project.Git.GitHubApp.AppID
				resp.GitHubInstallationID = project.Git.GitHubApp.InstallationID
			}
			resp.IntegrationType = project.Git.Type
		}
		projects = append(projects, resp)
	}

	if s.projectStore != nil {
		dynamicRepos := s.projectStore.List()
		for _, project := range dynamicRepos {
			if s.cfg.GetProject(project.Name) != nil {
				continue
			}

			resp := ProjectResponse{
				Name:                       project.Name,
				URL:                        project.URL,
				Branch:                     project.Branch,
				IgnorePaths:                project.IgnorePaths,
				Schedule:                   project.Schedule,
				CancelInflightOnNewTrigger: project.CancelInflightOnNewTrigger,
				AuthType:                   project.Git.Type,
				IntegrationID:              project.IntegrationID,
				Source:                     "dynamic",
				CreatedAt:                  project.CreatedAt.Format("2006-01-02T15:04:05Z"),
				UpdatedAt:                  project.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if project.Git.GitHubApp != nil {
				resp.GitHubAppID = project.Git.GitHubApp.AppID
				resp.GitHubInstallationID = project.Git.GitHubApp.InstallationID
			}
			if project.IntegrationID != "" {
				if name, typ, ok := s.lookupIntegrationMeta(project.IntegrationID); ok {
					resp.IntegrationName = name
					resp.IntegrationType = typ
				}
			} else if project.Git.Type != "" {
				resp.IntegrationType = project.Git.Type
			}
			projects = append(projects, resp)
		}
	}

	writeJSON(w, http.StatusOK, projects)
}

// handleGetSettingsRepo returns a single project by name.
func (s *Server) handleGetSettingsRepo(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")

	if project := s.cfg.GetProject(projectName); project != nil {
		resp := ProjectResponse{
			Name:                       project.Name,
			URL:                        project.URL,
			Branch:                     project.Branch,
			IgnorePaths:                project.IgnorePaths,
			Schedule:                   project.Schedule,
			CancelInflightOnNewTrigger: project.CancelInflightEnabled(),
			Source:                     "config",
		}
		if project.Git != nil {
			resp.AuthType = project.Git.Type
			if project.Git.GitHubApp != nil {
				resp.GitHubAppID = project.Git.GitHubApp.AppID
				resp.GitHubInstallationID = project.Git.GitHubApp.InstallationID
			}
			resp.IntegrationType = project.Git.Type
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if s.projectStore != nil {
		project, err := s.projectStore.Get(projectName)
		if err == nil {
			resp := ProjectResponse{
				Name:                       project.Name,
				URL:                        project.URL,
				Branch:                     project.Branch,
				IgnorePaths:                project.IgnorePaths,
				Schedule:                   project.Schedule,
				CancelInflightOnNewTrigger: project.CancelInflightOnNewTrigger,
				AuthType:                   project.Git.Type,
				IntegrationID:              project.IntegrationID,
				Source:                     "dynamic",
				CreatedAt:                  project.CreatedAt.Format("2006-01-02T15:04:05Z"),
				UpdatedAt:                  project.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if project.Git.GitHubApp != nil {
				resp.GitHubAppID = project.Git.GitHubApp.AppID
				resp.GitHubInstallationID = project.Git.GitHubApp.InstallationID
			}
			if project.IntegrationID != "" {
				if name, typ, ok := s.lookupIntegrationMeta(project.IntegrationID); ok {
					resp.IntegrationName = name
					resp.IntegrationType = typ
				}
			} else if project.Git.Type != "" {
				resp.IntegrationType = project.Git.Type
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
}

// handleCreateSettingsRepo creates a new project configuration.
func (s *Server) handleCreateSettingsRepo(w http.ResponseWriter, r *http.Request) {
	if s.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic project management not enabled",
		})
		return
	}

	var req ProjectRequest
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
	integrationID := strings.TrimSpace(derefString(req.IntegrationID))
	if req.AuthType == "" && integrationID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "integration_id is required"})
		return
	}

	if !isValidProjectName(req.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "name must contain only alphanumeric characters, hyphens, and underscores",
		})
		return
	}

	if s.cfg.GetProject(req.Name) != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "project name conflicts with static configuration",
		})
		return
	}

	entry := &secrets.ProjectEntry{
		Name:                       req.Name,
		URL:                        req.URL,
		Branch:                     derefString(req.Branch),
		IgnorePaths:                req.IgnorePaths,
		Schedule:                   derefString(req.Schedule),
		CancelInflightOnNewTrigger: derefBool(req.CancelInflightOnNewTrigger, true),
		Git:                        secrets.ProjectGitConfig{},
	}

	var creds *secrets.ProjectCredentials

	if integrationID != "" {
		integration, err := s.getIntegration(integrationID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "integration_id not found",
			})
			return
		}
		entry.IntegrationID = integration.ID
	} else {
		creds = &secrets.ProjectCredentials{}
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
			entry.Git.GitHubApp = &secrets.ProjectGitHubApp{
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

	if err := s.projectStore.Add(entry, creds); err != nil {
		if errors.Is(err, secrets.ErrProjectAlreadyExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "project already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if entry.Schedule != "" && s.onProjectAdded != nil {
		s.onProjectAdded(req.Name, entry.Schedule)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

// handleUpdateSettingsRepo updates an existing project configuration.
func (s *Server) handleUpdateSettingsRepo(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")

	if s.cfg.GetProject(projectName) != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cannot modify project defined in static configuration",
		})
		return
	}

	if s.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic project management not enabled",
		})
		return
	}

	existing, err := s.projectStore.Get(projectName)
	if err != nil {
		if errors.Is(err, secrets.ErrProjectNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var req ProjectRequest
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
	integrationID := existing.IntegrationID
	if req.IntegrationID != nil {
		integrationID = strings.TrimSpace(*req.IntegrationID)
	}
	if req.AuthType == "" && integrationID == "" {
		req.AuthType = existing.Git.Type
	}

	entry := &secrets.ProjectEntry{
		Name:                       existing.Name,
		URL:                        req.URL,
		Branch:                     existing.Branch,
		IgnorePaths:                existing.IgnorePaths,
		Schedule:                   existing.Schedule,
		CancelInflightOnNewTrigger: existing.CancelInflightOnNewTrigger,
		IntegrationID:              integrationID,
		Git:                        secrets.ProjectGitConfig{Type: req.AuthType},
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
	integrationChanged := integrationID != existing.IntegrationID

	var creds *secrets.ProjectCredentials
	if integrationID != "" {
		if _, err := s.getIntegration(integrationID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "integration_id not found",
			})
			return
		}
		entry.Git = secrets.ProjectGitConfig{}
		authChanged = false
		creds = &secrets.ProjectCredentials{}
	} else if req.GitHubPrivateKey != "" || req.SSHPrivateKey != "" || req.HTTPSToken != "" {
		creds = &secrets.ProjectCredentials{}
		switch req.AuthType {
		case "github_app":
			if req.GitHubAppID == 0 || req.GitHubInstallationID == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "github_app_id and github_installation_id are required for github_app auth",
				})
				return
			}
			entry.Git.GitHubApp = &secrets.ProjectGitHubApp{
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
				entry.Git.GitHubApp = &secrets.ProjectGitHubApp{}
			}
			entry.Git.GitHubApp.AppID = req.GitHubAppID
		}
		if req.GitHubInstallationID != 0 {
			if entry.Git.GitHubApp == nil {
				entry.Git.GitHubApp = &secrets.ProjectGitHubApp{}
			}
			entry.Git.GitHubApp.InstallationID = req.GitHubInstallationID
		}
	}
	if (authChanged || integrationChanged) && integrationID == "" && creds == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "credentials are required when changing auth_type",
		})
		return
	}

	if err := s.projectStore.Update(projectName, entry, creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if s.onProjectUpdated != nil {
		s.onProjectUpdated(entry.Name, entry.Schedule)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleDeleteSettingsRepo deletes a project configuration.
func (s *Server) handleDeleteSettingsRepo(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")

	if s.cfg.GetProject(projectName) != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cannot delete project defined in static configuration",
		})
		return
	}

	if s.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "dynamic project management not enabled",
		})
		return
	}

	if err := s.projectStore.Delete(projectName); err != nil {
		if errors.Is(err, secrets.ErrProjectNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if s.onProjectDeleted != nil {
		s.onProjectDeleted(projectName)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestProjectConnection tests the connection to a project.
func (s *Server) handleTestProjectConnection(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !isValidProjectName(projectName) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project name"})
		return
	}

	projectCfg, err := s.getProjectConfig(projectName)
	if err != nil {
		if errors.Is(err, secrets.ErrProjectNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.sanitizeErrorMessage(err.Error())})
		return
	}
	if projectCfg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	testCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	auth, err := gitauth.AuthMethod(testCtx, projectCfg)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.sanitizeErrorMessage(err.Error())})
		return
	}

	cloneURL := strings.TrimSpace(projectCfg.EffectiveCloneURL())
	if cloneURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project URL is empty"})
		return
	}

	remote := git.NewRemote(memory.NewStorage(), &gitcfg.RemoteConfig{
		Name: "origin",
		URLs: []string{cloneURL},
	})
	refs, err := remote.ListContext(testCtx, &git.ListOptions{Auth: auth})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.sanitizeErrorMessage(err.Error())})
		return
	}

	branch := strings.TrimSpace(projectCfg.Branch)
	if branch != "" {
		expectedRef := plumbing.NewBranchReferenceName(branch)
		found := false
		for _, ref := range refs {
			if ref.Name() == expectedRef {
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("branch %q not found in remote", branch),
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "connection successful",
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
	if s.projectStore != nil {
		for _, project := range s.projectStore.List() {
			if project.IntegrationID == id {
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
