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
	// ReposFileName is the filename for storing dynamic repo configurations.
	ReposFileName = "repos.json"
)

var (
	ErrRepoNotFound      = errors.New("repository not found")
	ErrRepoAlreadyExists = errors.New("repository already exists")
)

// RepoCredentials holds the sensitive credentials for a repository.
// These are encrypted before storage.
type RepoCredentials struct {
	// For github_app auth
	GitHubAppPrivateKey string `json:"github_app_private_key,omitempty"`

	// For ssh auth
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`

	// For https auth
	HTTPSToken    string `json:"https_token,omitempty"`
	HTTPSUsername string `json:"https_username,omitempty"`
}

// RepoGitHubApp holds GitHub App configuration (non-sensitive parts).
type RepoGitHubApp struct {
	AppID          int64 `json:"app_id"`
	InstallationID int64 `json:"installation_id"`
}

// RepoGitConfig holds git authentication configuration.
type RepoGitConfig struct {
	Type      string         `json:"type"` // "https", "ssh", "github_app"
	GitHubApp *RepoGitHubApp `json:"github_app,omitempty"`
}

// RepoEntry represents a repository configuration as stored in the repo store.
type RepoEntry struct {
	Name                       string        `json:"name"`
	URL                        string        `json:"url"`
	Branch                     string        `json:"branch,omitempty"`
	IgnorePaths                []string      `json:"ignore_paths,omitempty"`
	Git                        RepoGitConfig `json:"git"`
	Schedule                   string        `json:"schedule,omitempty"`
	CancelInflightOnNewTrigger bool          `json:"cancel_inflight_on_new_trigger,omitempty"`

	// EncryptedCredentials holds the encrypted credentials blob.
	EncryptedCredentials string `json:"encrypted_credentials,omitempty"`

	// Metadata
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// repoStoreData is the on-disk format for the repo store.
type repoStoreData struct {
	Version int          `json:"version"`
	Repos   []*RepoEntry `json:"repos"`
}

// RepoStore manages encrypted repository configurations.
type RepoStore struct {
	dataDir   string
	encryptor *Encryptor
	mu        sync.RWMutex

	// In-memory cache
	repos map[string]*RepoEntry
}

// NewRepoStore creates a new RepoStore.
func NewRepoStore(dataDir string, encryptor *Encryptor) *RepoStore {
	return &RepoStore{
		dataDir:   dataDir,
		encryptor: encryptor,
		repos:     make(map[string]*RepoEntry),
	}
}

// Load reads the repo store from disk into memory.
func (rs *RepoStore) Load() error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	data, err := os.ReadFile(rs.filePath())
	if os.IsNotExist(err) {
		// No repos file yet, start empty
		rs.repos = make(map[string]*RepoEntry)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read repos file: %w", err)
	}

	var storeData repoStoreData
	if err := json.Unmarshal(data, &storeData); err != nil {
		return fmt.Errorf("failed to parse repos file: %w", err)
	}

	rs.repos = make(map[string]*RepoEntry, len(storeData.Repos))
	for _, repo := range storeData.Repos {
		rs.repos[repo.Name] = repo
	}

	return nil
}

// Save writes the repo store to disk.
func (rs *RepoStore) Save() error {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	return rs.saveLocked()
}

func (rs *RepoStore) saveLocked() error {
	repos := make([]*RepoEntry, 0, len(rs.repos))
	for _, repo := range rs.repos {
		repos = append(repos, repo)
	}

	storeData := repoStoreData{
		Version: 1,
		Repos:   repos,
	}

	data, err := json.MarshalIndent(storeData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal repos: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(rs.dataDir, 0750); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Write atomically via temp file
	tmpPath := rs.filePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write repos file: %w", err)
	}

	if err := os.Rename(tmpPath, rs.filePath()); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename repos file: %w", err)
	}

	return nil
}

// filePath returns the path to the repos file.
func (rs *RepoStore) filePath() string {
	return filepath.Join(rs.dataDir, ReposFileName)
}

// List returns all repository entries (without decrypted credentials).
func (rs *RepoStore) List() []*RepoEntry {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	repos := make([]*RepoEntry, 0, len(rs.repos))
	for _, repo := range rs.repos {
		// Return copy without credentials
		entry := *repo
		entry.EncryptedCredentials = "" // Don't expose encrypted blob
		repos = append(repos, &entry)
	}
	return repos
}

// Get returns a repository entry by name (without decrypted credentials).
func (rs *RepoStore) Get(name string) (*RepoEntry, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	repo, ok := rs.repos[name]
	if !ok {
		return nil, ErrRepoNotFound
	}

	// Return copy without credentials
	entry := *repo
	entry.EncryptedCredentials = ""
	return &entry, nil
}

// GetWithCredentials returns a repository entry with decrypted credentials.
func (rs *RepoStore) GetWithCredentials(name string) (*RepoEntry, *RepoCredentials, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	repo, ok := rs.repos[name]
	if !ok {
		return nil, nil, ErrRepoNotFound
	}

	entry := *repo
	entry.EncryptedCredentials = ""

	var creds RepoCredentials
	if repo.EncryptedCredentials != "" {
		decrypted, err := rs.encryptor.Decrypt(repo.EncryptedCredentials)
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
func (rs *RepoStore) Add(entry *RepoEntry, creds *RepoCredentials) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if _, exists := rs.repos[entry.Name]; exists {
		return ErrRepoAlreadyExists
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

	rs.repos[entry.Name] = entry

	return rs.saveLocked()
}

// Update updates an existing repository.
func (rs *RepoStore) Update(name string, entry *RepoEntry, creds *RepoCredentials) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	existing, ok := rs.repos[name]
	if !ok {
		return ErrRepoNotFound
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
		delete(rs.repos, name)
	}
	rs.repos[entry.Name] = entry

	return rs.saveLocked()
}

// Delete removes a repository by name.
func (rs *RepoStore) Delete(name string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if _, ok := rs.repos[name]; !ok {
		return ErrRepoNotFound
	}

	delete(rs.repos, name)

	return rs.saveLocked()
}

// Exists returns true if a repository with the given name exists.
func (rs *RepoStore) Exists(name string) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	_, ok := rs.repos[name]
	return ok
}
