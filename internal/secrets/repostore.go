package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// ProjectsFileName is the filename for storing dynamic project configurations.
	ProjectsFileName = "projects.json"
)

var (
	ErrProjectNotFound      = errors.New("project not found")
	ErrProjectAlreadyExists = errors.New("project already exists")
)

// ProjectCredentials holds the sensitive credentials for a repository.
// These are encrypted before storage.
type ProjectCredentials struct {
	// For github_app auth
	GitHubAppPrivateKey string `json:"github_app_private_key,omitempty"`

	// For ssh auth
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`

	// For https auth
	HTTPSToken    string `json:"https_token,omitempty"`
	HTTPSUsername string `json:"https_username,omitempty"`
}

// ProjectGitHubApp holds GitHub App configuration (non-sensitive parts).
type ProjectGitHubApp struct {
	AppID          int64 `json:"app_id"`
	InstallationID int64 `json:"installation_id"`
}

// ProjectGitConfig holds git authentication configuration.
type ProjectGitConfig struct {
	Type      string            `json:"type"` // "https", "ssh", "github_app"
	GitHubApp *ProjectGitHubApp `json:"github_app,omitempty"`
}

// ProjectEntry represents a repository configuration as stored in the project store.
type ProjectEntry struct {
	Name                       string           `json:"name"`
	URL                        string           `json:"url"`
	Branch                     string           `json:"branch,omitempty"`
	IgnorePaths                []string         `json:"ignore_paths,omitempty"`
	IntegrationID              string           `json:"integration_id,omitempty"`
	Git                        ProjectGitConfig `json:"git"`
	Schedule                   string           `json:"schedule,omitempty"`
	CancelInflightOnNewTrigger bool             `json:"cancel_inflight_on_new_trigger,omitempty"`

	// EncryptedCredentials holds the encrypted credentials blob.
	EncryptedCredentials string `json:"encrypted_credentials,omitempty"`

	// Metadata
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// projectStoreData is the on-disk format for the project store.
type projectStoreData struct {
	Version  int             `json:"version"`
	Projects []*ProjectEntry `json:"projects"`
}

// ProjectStore manages encrypted repository configurations.
type ProjectStore struct {
	dataDir   string
	encryptor *Encryptor
	mu        sync.RWMutex

	// In-memory cache
	projects map[string]*ProjectEntry
}

// NewProjectStore creates a new ProjectStore.
func NewProjectStore(dataDir string, encryptor *Encryptor) *ProjectStore {
	return &ProjectStore{
		dataDir:   dataDir,
		encryptor: encryptor,
		projects:  make(map[string]*ProjectEntry),
	}
}

// Load reads the project store from disk into memory.
func (rs *ProjectStore) Load() error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	data, err := os.ReadFile(rs.filePath())
	if os.IsNotExist(err) {
		// No projects file yet, start empty
		rs.projects = make(map[string]*ProjectEntry)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read projects file: %w", err)
	}

	var storeData projectStoreData
	if err := json.Unmarshal(data, &storeData); err != nil {
		return fmt.Errorf("failed to parse projects file: %w", err)
	}

	rs.projects = make(map[string]*ProjectEntry, len(storeData.Projects))
	for _, project := range storeData.Projects {
		rs.projects[project.Name] = project
	}

	return nil
}

// Save writes the project store to disk.
func (rs *ProjectStore) Save() error {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	return rs.saveLocked()
}

func (rs *ProjectStore) saveLocked() error {
	projects := make([]*ProjectEntry, 0, len(rs.projects))
	for _, project := range rs.projects {
		projects = append(projects, project)
	}

	storeData := projectStoreData{
		Version:  1,
		Projects: projects,
	}

	data, err := json.MarshalIndent(storeData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal projects: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(rs.dataDir, 0750); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Write atomically via temp file
	tmpPath := rs.filePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write projects file: %w", err)
	}

	if err := os.Rename(tmpPath, rs.filePath()); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename projects file: %w", err)
	}

	return nil
}

// filePath returns the path to the projects file.
func (rs *ProjectStore) filePath() string {
	return filepath.Join(rs.dataDir, ProjectsFileName)
}

// List returns all repository entries (without decrypted credentials).
func (rs *ProjectStore) List() []*ProjectEntry {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	projects := make([]*ProjectEntry, 0, len(rs.projects))
	for _, project := range rs.projects {
		// Return copy without credentials
		entry := *project
		entry.EncryptedCredentials = "" // Don't expose encrypted blob
		projects = append(projects, &entry)
	}
	return projects
}

// Get returns a repository entry by name (without decrypted credentials).
func (rs *ProjectStore) Get(name string) (*ProjectEntry, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	project, ok := rs.projects[name]
	if !ok {
		return nil, ErrProjectNotFound
	}

	// Return copy without credentials
	entry := *project
	entry.EncryptedCredentials = ""
	return &entry, nil
}

// GetWithCredentials returns a repository entry with decrypted credentials.
func (rs *ProjectStore) GetWithCredentials(name string) (*ProjectEntry, *ProjectCredentials, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	project, ok := rs.projects[name]
	if !ok {
		return nil, nil, ErrProjectNotFound
	}

	entry := *project
	entry.EncryptedCredentials = ""

	var creds ProjectCredentials
	if project.EncryptedCredentials != "" {
		decrypted, err := rs.encryptor.Decrypt(project.EncryptedCredentials)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decrypt credentials: %w", err)
		}
		if err := json.Unmarshal(decrypted, &creds); err != nil {
			return nil, nil, fmt.Errorf("failed to parse credentials: %w", err)
		}
	}

	return &entry, &creds, nil
}

// Add adds a new repository with encrypted credentials.
func (rs *ProjectStore) Add(entry *ProjectEntry, creds *ProjectCredentials) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if _, exists := rs.projects[entry.Name]; exists {
		return ErrProjectAlreadyExists
	}

	// Encrypt credentials
	if creds != nil {
		credsJSON, err := json.Marshal(creds)
		if err != nil {
			return fmt.Errorf("failed to marshal credentials: %w", err)
		}
		encrypted, err := rs.encryptor.Encrypt(credsJSON)
		if err != nil {
			return fmt.Errorf("failed to encrypt credentials: %w", err)
		}
		entry.EncryptedCredentials = encrypted
	}

	now := time.Now().UTC()
	entry.CreatedAt = now
	entry.UpdatedAt = now

	rs.projects[entry.Name] = entry

	return rs.saveLocked()
}

// Update updates an existing repository.
func (rs *ProjectStore) Update(name string, entry *ProjectEntry, creds *ProjectCredentials) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	existing, ok := rs.projects[name]
	if !ok {
		return ErrProjectNotFound
	}

	// Preserve created timestamp
	entry.CreatedAt = existing.CreatedAt
	entry.UpdatedAt = time.Now().UTC()

	// Encrypt credentials if provided, otherwise keep existing
	if creds != nil {
		credsJSON, err := json.Marshal(creds)
		if err != nil {
			return fmt.Errorf("failed to marshal credentials: %w", err)
		}
		encrypted, err := rs.encryptor.Encrypt(credsJSON)
		if err != nil {
			return fmt.Errorf("failed to encrypt credentials: %w", err)
		}
		entry.EncryptedCredentials = encrypted
	} else {
		entry.EncryptedCredentials = existing.EncryptedCredentials
	}

	// Handle name change
	if name != entry.Name {
		delete(rs.projects, name)
	}
	rs.projects[entry.Name] = entry

	return rs.saveLocked()
}

// Delete removes a repository by name.
func (rs *ProjectStore) Delete(name string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if _, ok := rs.projects[name]; !ok {
		return ErrProjectNotFound
	}

	delete(rs.projects, name)

	return rs.saveLocked()
}

// Exists returns true if a repository with the given name exists.
func (rs *ProjectStore) Exists(name string) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	_, ok := rs.projects[name]
	return ok
}
