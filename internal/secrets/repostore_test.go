package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestProjectStore(t *testing.T) (*ProjectStore, string) {
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

	store := NewProjectStore(tmpDir, enc)
	return store, tmpDir
}

func TestProjectStore_AddAndGet(t *testing.T) {
	store, tmpDir := setupTestProjectStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &ProjectEntry{
		Name:   "test-project",
		URL:    "https://github.com/example/project.git",
		Branch: "main",
		Git: ProjectGitConfig{
			Type: "github_app",
			GitHubApp: &ProjectGitHubApp{
				AppID:          12345,
				InstallationID: 67890,
			},
		},
	}
	creds := &ProjectCredentials{
		GitHubAppPrivateKey: "-----BEGIN RSA PRIVATE KEY-----\ntest\n-----END RSA PRIVATE KEY-----",
	}

	// Add project
	if err := store.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Get without credentials
	got, err := store.Get("test-project")
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
	gotEntry, gotCreds, err := store.GetWithCredentials("test-project")
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

func TestProjectStore_AddDuplicate(t *testing.T) {
	store, tmpDir := setupTestProjectStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &ProjectEntry{
		Name: "test-project",
		URL:  "https://github.com/example/project.git",
		Git:  ProjectGitConfig{Type: "https"},
	}

	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	err := store.Add(entry, nil)
	if err != ErrProjectAlreadyExists {
		t.Errorf("Add() duplicate error = %v, want %v", err, ErrProjectAlreadyExists)
	}
}

func TestProjectStore_Update(t *testing.T) {
	store, tmpDir := setupTestProjectStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &ProjectEntry{
		Name:   "test-project",
		URL:    "https://github.com/example/project.git",
		Branch: "main",
		Git:    ProjectGitConfig{Type: "https"},
	}
	creds := &ProjectCredentials{
		HTTPSToken: "old-token",
	}

	if err := store.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Update with new credentials
	entry.Branch = "develop"
	newCreds := &ProjectCredentials{
		HTTPSToken: "new-token",
	}

	if err := store.Update("test-project", entry, newCreds); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, gotCreds, err := store.GetWithCredentials("test-project")
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

func TestProjectStore_UpdateKeepCredentials(t *testing.T) {
	store, tmpDir := setupTestProjectStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &ProjectEntry{
		Name: "test-project",
		URL:  "https://github.com/example/project.git",
		Git:  ProjectGitConfig{Type: "https"},
	}
	creds := &ProjectCredentials{
		HTTPSToken: "secret-token",
	}

	if err := store.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Update without credentials (should keep existing)
	entry.Branch = "develop"
	if err := store.Update("test-project", entry, nil); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	_, gotCreds, err := store.GetWithCredentials("test-project")
	if err != nil {
		t.Fatalf("GetWithCredentials() error = %v", err)
	}
	if gotCreds.HTTPSToken != "secret-token" {
		t.Errorf("Update() should keep existing credentials")
	}
}

func TestProjectStore_Delete(t *testing.T) {
	store, tmpDir := setupTestProjectStore(t)
	defer os.RemoveAll(tmpDir)

	entry := &ProjectEntry{
		Name: "test-project",
		URL:  "https://github.com/example/project.git",
		Git:  ProjectGitConfig{Type: "https"},
	}

	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := store.Delete("test-project"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if store.Exists("test-project") {
		t.Error("Exists() should return false after delete")
	}

	_, err := store.Get("test-project")
	if err != ErrProjectNotFound {
		t.Errorf("Get() after delete error = %v, want %v", err, ErrProjectNotFound)
	}
}

func TestProjectStore_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "repostore-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	key, _ := GenerateKey()
	enc, _ := NewEncryptor(key)

	// Create store and add project
	store1 := NewProjectStore(tmpDir, enc)
	entry := &ProjectEntry{
		Name: "test-project",
		URL:  "https://github.com/example/project.git",
		Git:  ProjectGitConfig{Type: "https"},
	}
	creds := &ProjectCredentials{
		HTTPSToken: "secret",
	}

	if err := store1.Add(entry, creds); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmpDir, ProjectsFileName)); os.IsNotExist(err) {
		t.Fatal("projects.json should exist after Add()")
	}

	// Create new store instance and load
	store2 := NewProjectStore(tmpDir, enc)
	if err := store2.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify data persisted
	got, gotCreds, err := store2.GetWithCredentials("test-project")
	if err != nil {
		t.Fatalf("GetWithCredentials() error = %v", err)
	}
	if got.Name != "test-project" {
		t.Errorf("Persistence: name = %v, want %v", got.Name, "test-project")
	}
	if gotCreds.HTTPSToken != "secret" {
		t.Errorf("Persistence: token = %v, want %v", gotCreds.HTTPSToken, "secret")
	}
}

func TestProjectStore_List(t *testing.T) {
	store, tmpDir := setupTestProjectStore(t)
	defer os.RemoveAll(tmpDir)

	// Add multiple projects
	for _, name := range []string{"project-a", "project-b", "project-c"} {
		entry := &ProjectEntry{
			Name: name,
			URL:  "https://github.com/example/" + name + ".git",
			Git:  ProjectGitConfig{Type: "https"},
		}
		if err := store.Add(entry, nil); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	projects := store.List()
	if len(projects) != 3 {
		t.Errorf("List() returned %d projects, want 3", len(projects))
	}

	// Verify no encrypted credentials in list
	for _, project := range projects {
		if project.EncryptedCredentials != "" {
			t.Errorf("List() should not include encrypted credentials")
		}
	}
}
