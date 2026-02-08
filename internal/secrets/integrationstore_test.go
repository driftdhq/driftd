package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestIntegrationStore(t *testing.T) (*IntegrationStore, string) {
	t.Helper()

	tmpDir := t.TempDir()
	store := NewIntegrationStore(tmpDir)
	if err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return store, tmpDir
}

func TestIntegrationStoreAddGetList(t *testing.T) {
	store, _ := setupTestIntegrationStore(t)

	entry := &IntegrationEntry{
		ID:   "int-1",
		Name: "primary",
		Type: "github_app",
		GitHubApp: &IntegrationGitHubApp{
			AppID:          1,
			InstallationID: 2,
			PrivateKeyPath: "/tmp/key.pem",
		},
	}

	if err := store.Add(entry); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := store.Get("int-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "primary" || got.Type != "github_app" {
		t.Fatalf("unexpected entry: %+v", got)
	}

	list := store.List()
	if len(list) != 1 || list[0].ID != "int-1" {
		t.Fatalf("expected 1 entry, got %+v", list)
	}
}

func TestIntegrationStoreUpdateDelete(t *testing.T) {
	store, _ := setupTestIntegrationStore(t)

	entry := &IntegrationEntry{
		ID:   "int-1",
		Name: "primary",
		Type: "ssh",
		SSH: &IntegrationSSH{
			KeyPath:        "/tmp/id_rsa",
			KnownHostsPath: "/tmp/known_hosts",
		},
	}
	if err := store.Add(entry); err != nil {
		t.Fatalf("add: %v", err)
	}

	updated := &IntegrationEntry{
		Name: "updated",
		Type: "https",
		HTTPS: &IntegrationHTTPS{
			Username: "token-user",
			TokenEnv: "TOKEN_ENV",
		},
	}
	if err := store.Update("int-1", updated); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := store.Get("int-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "updated" || got.Type != "https" {
		t.Fatalf("unexpected updated entry: %+v", got)
	}

	if err := store.Delete("int-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get("int-1"); err != ErrIntegrationNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestIntegrationStorePersistence(t *testing.T) {
	store, tmpDir := setupTestIntegrationStore(t)

	entry := &IntegrationEntry{
		ID:   "int-1",
		Name: "primary",
		Type: "https",
		HTTPS: &IntegrationHTTPS{
			Username: "user",
			TokenEnv: "TOKEN_ENV",
		},
	}
	if err := store.Add(entry); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Load a new store from disk
	loaded := NewIntegrationStore(tmpDir)
	if err := loaded.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	got, err := loaded.Get("int-1")
	if err != nil {
		t.Fatalf("get loaded: %v", err)
	}
	if got.Name != "primary" {
		t.Fatalf("unexpected loaded entry: %+v", got)
	}

	// Ensure file exists
	if _, err := os.Stat(filepath.Join(tmpDir, IntegrationsFileName)); err != nil {
		t.Fatalf("expected integrations file: %v", err)
	}
}
