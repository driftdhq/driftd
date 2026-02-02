package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupWorkspaceArtifacts(t *testing.T) {
	root := t.TempDir()

	dirs := []string{
		filepath.Join(root, ".terraform"),
		filepath.Join(root, ".terragrunt-cache"),
		filepath.Join(root, "terraform.tfstate.d"),
		filepath.Join(root, "envs", "prod", ".terraform"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "placeholder"), []byte("x"), 0644); err != nil {
			t.Fatalf("write placeholder: %v", err)
		}
	}

	files := []string{
		filepath.Join(root, ".terraform.lock.hcl"),
		filepath.Join(root, "crash.log"),
		filepath.Join(root, ".terraform.tfstate.lock.info"),
		filepath.Join(root, "errored.tfstate"),
		filepath.Join(root, "terraform.tfstate"),
		filepath.Join(root, "terraform.tfstate.backup"),
		filepath.Join(root, "envs", "prod", "state.tfstate"),
	}
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatalf("mkdir for file: %v", err)
		}
		if err := os.WriteFile(file, []byte("state"), 0644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	keepFiles := []string{
		filepath.Join(root, "main.tf"),
		filepath.Join(root, "README.md"),
		filepath.Join(root, "envs", "prod", "outputs.tf"),
	}
	for _, file := range keepFiles {
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatalf("mkdir keep: %v", err)
		}
		if err := os.WriteFile(file, []byte("keep"), 0644); err != nil {
			t.Fatalf("write keep: %v", err)
		}
	}

	if err := CleanupWorkspaceArtifacts(root); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected dir removed: %s", dir)
		}
	}
	for _, file := range files {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("expected file removed: %s", file)
		}
	}
	for _, file := range keepFiles {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("expected keep file present: %s (%v)", file, err)
		}
	}
}

func TestCleanupSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	targetDir := t.TempDir()
	targetFile := filepath.Join(targetDir, "crash.log")
	if err := os.WriteFile(targetFile, []byte("x"), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	linkPath := filepath.Join(root, "crash.log")
	if err := os.Symlink(targetFile, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := CleanupWorkspaceArtifacts(root); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("expected symlink preserved: %v", err)
	}
	if _, err := os.Stat(targetFile); err != nil {
		t.Fatalf("expected target preserved: %v", err)
	}
}
