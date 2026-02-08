package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	// IntegrationsFileName is the filename for storing dynamic integration configurations.
	IntegrationsFileName = "integrations.json"
)

var (
	ErrIntegrationNotFound      = errors.New("integration not found")
	ErrIntegrationAlreadyExists = errors.New("integration already exists")
)

// IntegrationGitHubApp holds GitHub App integration configuration.
type IntegrationGitHubApp struct {
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	PrivateKeyPath string `json:"private_key_path,omitempty"`
	PrivateKeyEnv  string `json:"private_key_env,omitempty"`
	APIBaseURL     string `json:"api_base_url,omitempty"`
}

// IntegrationSSH holds SSH integration configuration.
type IntegrationSSH struct {
	KeyPath               string `json:"key_path,omitempty"`
	KeyEnv                string `json:"key_env,omitempty"`
	KeyPassphraseEnv      string `json:"key_passphrase_env,omitempty"`
	KnownHostsPath        string `json:"known_hosts_path,omitempty"`
	InsecureIgnoreHostKey bool   `json:"insecure_ignore_host_key,omitempty"`
}

// IntegrationHTTPS holds HTTPS integration configuration.
type IntegrationHTTPS struct {
	Username string `json:"username,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

// IntegrationEntry represents an integration configuration as stored in the integration store.
type IntegrationEntry struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Type      string                `json:"type"` // "github_app", "ssh", "https"
	GitHubApp *IntegrationGitHubApp `json:"github_app,omitempty"`
	SSH       *IntegrationSSH       `json:"ssh,omitempty"`
	HTTPS     *IntegrationHTTPS     `json:"https,omitempty"`

	// Metadata
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type integrationStoreData struct {
	Version      int                 `json:"version"`
	Integrations []*IntegrationEntry `json:"integrations"`
}

// IntegrationStore manages integration configurations.
type IntegrationStore struct {
	dataDir string
	mu      sync.RWMutex

	integrations map[string]*IntegrationEntry
}

// NewIntegrationStore creates a new IntegrationStore.
func NewIntegrationStore(dataDir string) *IntegrationStore {
	return &IntegrationStore{
		dataDir:      dataDir,
		integrations: make(map[string]*IntegrationEntry),
	}
}

// Load reads the integration store from disk into memory.
func (s *IntegrationStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath())
	if os.IsNotExist(err) {
		s.integrations = make(map[string]*IntegrationEntry)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read integrations file: %w", err)
	}

	var storeData integrationStoreData
	if err := json.Unmarshal(data, &storeData); err != nil {
		return fmt.Errorf("failed to parse integrations file: %w", err)
	}

	s.integrations = make(map[string]*IntegrationEntry, len(storeData.Integrations))
	for _, entry := range storeData.Integrations {
		s.integrations[entry.ID] = entry
	}

	return nil
}

// Save writes the integration store to disk.
func (s *IntegrationStore) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.saveLocked()
}

func (s *IntegrationStore) saveLocked() error {
	entries := make([]*IntegrationEntry, 0, len(s.integrations))
	for _, entry := range s.integrations {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	storeData := integrationStoreData{
		Version:      1,
		Integrations: entries,
	}

	data, err := json.MarshalIndent(storeData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal integrations: %w", err)
	}

	if err := os.MkdirAll(s.dataDir, 0750); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	tmpPath := s.filePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write integrations file: %w", err)
	}

	if err := os.Rename(tmpPath, s.filePath()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename integrations file: %w", err)
	}

	return nil
}

func (s *IntegrationStore) filePath() string {
	return filepath.Join(s.dataDir, IntegrationsFileName)
}

// List returns all integration entries.
func (s *IntegrationStore) List() []*IntegrationEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]*IntegrationEntry, 0, len(s.integrations))
	for _, entry := range s.integrations {
		cpy := *entry
		entries = append(entries, &cpy)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

// Get returns an integration entry by ID.
func (s *IntegrationStore) Get(id string) (*IntegrationEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.integrations[id]
	if !ok {
		return nil, ErrIntegrationNotFound
	}
	cpy := *entry
	return &cpy, nil
}

// Add stores a new integration entry.
func (s *IntegrationStore) Add(entry *IntegrationEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry == nil || entry.ID == "" {
		return fmt.Errorf("integration entry and ID required")
	}
	if _, exists := s.integrations[entry.ID]; exists {
		return ErrIntegrationAlreadyExists
	}

	now := time.Now()
	entry.CreatedAt = now
	entry.UpdatedAt = now
	s.integrations[entry.ID] = entry

	return s.saveLocked()
}

// Update updates an existing integration entry.
func (s *IntegrationStore) Update(id string, entry *IntegrationEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.integrations[id]
	if !ok {
		return ErrIntegrationNotFound
	}

	entry.ID = existing.ID
	entry.CreatedAt = existing.CreatedAt
	entry.UpdatedAt = time.Now()
	s.integrations[id] = entry

	return s.saveLocked()
}

// Delete removes an integration entry.
func (s *IntegrationStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.integrations[id]; !ok {
		return ErrIntegrationNotFound
	}
	delete(s.integrations, id)
	return s.saveLocked()
}

// Exists returns true if an integration ID exists.
func (s *IntegrationStore) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.integrations[id]
	return ok
}
