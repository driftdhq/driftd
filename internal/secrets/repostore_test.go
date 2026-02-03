package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestRepoStore(t *testing.T) (*RepoStore, string) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "repostore-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	enc, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	store := NewRepoStore(tmpDir, enc)
	return store, tmpDir
}

func TestRepoStore_AddAndGet(t *testing.T) {
	store, tmpDir := setupTestRepoStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &RepoEntry{
		Name:   "test-repo",
		URL:    "https://github.com/example/repo.git",
		Branch: "main",
		Git: RepoGitConfig{
			Type: "github_app",
			GitHubApp: &RepoGitHubApp{
				AppID:          12345,
				InstallationID: 67890,
			},
		},
	}
	creds := &RepoCredentials{
		GitHubAppPrivateKey: "-----BEGIN RSA PRIVATE KEY-----\ntest\n-----END RSA PRIVATE KEY-----",
	}

	// Add repo
	if err := store.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Get without credentials
	got, err := store.Get("test-repo")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name != entry.Name {
		t.Errorf("Get() name = %v, want %v", got.Name, entry.Name)
	}
	if got.EncryptedCredentials != "" {
		t.Error("Get() should not return encrypted credentials")
	}

	// Get with credentials
	gotEntry, gotCreds, err := store.GetWithCredentials("test-repo")
	if err != nil {
		t.Fatalf("GetWithCredentials() error = %v", err)
	}
	if gotEntry.Name != entry.Name {
		t.Errorf("GetWithCredentials() name = %v, want %v", gotEntry.Name, entry.Name)
	}
	if gotCreds.GitHubAppPrivateKey != creds.GitHubAppPrivateKey {
		t.Errorf("GetWithCredentials() private key mismatch")
	}
}

func TestRepoStore_AddDuplicate(t *testing.T) {
	store, tmpDir := setupTestRepoStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &RepoEntry{
		Name: "test-repo",
		URL:  "https://github.com/example/repo.git",
		Git:  RepoGitConfig{Type: "https"},
	}

	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	err := store.Add(entry, nil)
	if err != ErrRepoAlreadyExists {
		t.Errorf("Add() duplicate error = %v, want %v", err, ErrRepoAlreadyExists)
	}
}

func TestRepoStore_Update(t *testing.T) {
	store, tmpDir := setupTestRepoStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &RepoEntry{
		Name:   "test-repo",
		URL:    "https://github.com/example/repo.git",
		Branch: "main",
		Git:    RepoGitConfig{Type: "https"},
	}
	creds := &RepoCredentials{
		HTTPSToken: "old-token",
	}

	if err := store.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Update with new credentials
	entry.Branch = "develop"
	newCreds := &RepoCredentials{
		HTTPSToken: "new-token",
	}

	if err := store.Update("test-repo", entry, newCreds); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, gotCreds, err := store.GetWithCredentials("test-repo")
	if err != nil {
		t.Fatalf("GetWithCredentials() error = %v", err)
	}
	if got.Branch != "develop" {
		t.Errorf("Update() branch = %v, want %v", got.Branch, "develop")
	}
	if gotCreds.HTTPSToken != "new-token" {
		t.Errorf("Update() token = %v, want %v", gotCreds.HTTPSToken, "new-token")
	}
}

func TestRepoStore_UpdateKeepCredentials(t *testing.T) {
	store, tmpDir := setupTestRepoStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &RepoEntry{
		Name: "test-repo",
		URL:  "https://github.com/example/repo.git",
		Git:  RepoGitConfig{Type: "https"},
	}
	creds := &RepoCredentials{
		HTTPSToken: "secret-token",
	}

	if err := store.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Update without credentials (should keep existing)
	entry.Branch = "develop"
	if err := store.Update("test-repo", entry, nil); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	_, gotCreds, err := store.GetWithCredentials("test-repo")
	if err != nil {
		t.Fatalf("GetWithCredentials() error = %v", err)
	}
	if gotCreds.HTTPSToken != "secret-token" {
		t.Errorf("Update() should keep existing credentials")
	}
}

func TestRepoStore_Delete(t *testing.T) {
	store, tmpDir := setupTestRepoStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &RepoEntry{
		Name: "test-repo",
		URL:  "https://github.com/example/repo.git",
		Git:  RepoGitConfig{Type: "https"},
	}

	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := store.Delete("test-repo"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if store.Exists("test-repo") {
		t.Error("Exists() should return false after delete")
	}

	_, err := store.Get("test-repo")
	if err != ErrRepoNotFound {
		t.Errorf("Get() after delete error = %v, want %v", err, ErrRepoNotFound)
	}
}

func TestRepoStore_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "repostore-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	key, _ := GenerateKey()
	enc, _ := NewEncryptor(key)

	// Create store and add repo
	store1 := NewRepoStore(tmpDir, enc)
	entry := &RepoEntry{
		Name: "test-repo",
		URL:  "https://github.com/example/repo.git",
		Git:  RepoGitConfig{Type: "https"},
	}
	creds := &RepoCredentials{
		HTTPSToken: "secret",
	}

	if err := store1.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmpDir, ReposFileName)); os.IsNotExist(err) {
		t.Fatal("repos.json should exist after Add()")
	}

	// Create new store instance and load
	store2 := NewRepoStore(tmpDir, enc)
	if err := store2.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify data persisted
	got, gotCreds, err := store2.GetWithCredentials("test-repo")
	if err != nil {
		t.Fatalf("GetWithCredentials() error = %v", err)
	}
	if got.Name != "test-repo" {
		t.Errorf("Persistence: name = %v, want %v", got.Name, "test-repo")
	}
	if gotCreds.HTTPSToken != "secret" {
		t.Errorf("Persistence: token = %v, want %v", gotCreds.HTTPSToken, "secret")
	}
}

func TestRepoStore_List(t *testing.T) {
	store, tmpDir := setupTestRepoStore(t)
	defer os.RemoveAll(tmpDir)

	// Add multiple repos
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		entry := &RepoEntry{
			Name: name,
			URL:  "https://github.com/example/" + name + ".git",
			Git:  RepoGitConfig{Type: "https"},
		}
		if err := store.Add(entry, nil); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	repos := store.List()
	if len(repos) != 3 {
		t.Errorf("List() returned %d repos, want 3", len(repos))
	}

	// Verify no encrypted credentials in list
	for _, repo := range repos {
		if repo.EncryptedCredentials != "" {
			t.Errorf("List() should not include encrypted credentials")
		}
	}
}
