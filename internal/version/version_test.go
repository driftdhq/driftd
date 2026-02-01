package version

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func ensureDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func TestDetectRootVersions(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".terraform-version"), "1.6.2")
	writeFile(t, filepath.Join(repo, ".terragrunt-version"), "0.56.4")

	stacks := []string{"envs/dev", "envs/prod"}
	for _, stack := range stacks {
		ensureDir(t, filepath.Join(repo, stack))
	}

	versions, err := Detect(repo, stacks)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	if versions.DefaultTerraform != "1.6.2" {
		t.Fatalf("expected default tf 1.6.2, got %q", versions.DefaultTerraform)
	}
	if versions.DefaultTerragrunt != "0.56.4" {
		t.Fatalf("expected default tg 0.56.4, got %q", versions.DefaultTerragrunt)
	}
	if len(versions.StackTerraform) != 0 || len(versions.StackTerragrunt) != 0 {
		t.Fatalf("expected no stack overrides")
	}
}

func TestDetectStackOverrides(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".terraform-version"), "1.6.2")
	writeFile(t, filepath.Join(repo, ".terragrunt-version"), "0.56.4")

	stacks := []string{"envs/dev", "envs/prod"}
	for _, stack := range stacks {
		ensureDir(t, filepath.Join(repo, stack))
	}
	writeFile(t, filepath.Join(repo, "envs/dev", ".terraform-version"), "1.5.7")
	writeFile(t, filepath.Join(repo, "envs/prod", ".terragrunt-version"), "0.55.0")

	versions, err := Detect(repo, stacks)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	if versions.DefaultTerraform != "1.6.2" {
		t.Fatalf("expected default tf 1.6.2, got %q", versions.DefaultTerraform)
	}
	if versions.DefaultTerragrunt != "0.56.4" {
		t.Fatalf("expected default tg 0.56.4, got %q", versions.DefaultTerragrunt)
	}
	if versions.StackTerraform["envs/dev"] != "1.5.7" {
		t.Fatalf("expected dev override 1.5.7, got %q", versions.StackTerraform["envs/dev"])
	}
	if versions.StackTerragrunt["envs/prod"] != "0.55.0" {
		t.Fatalf("expected prod tg override 0.55.0, got %q", versions.StackTerragrunt["envs/prod"])
	}
}

func TestCollapseIfSingle(t *testing.T) {
	repo := t.TempDir()
	stacks := []string{"envs/dev", "envs/prod"}
	for _, stack := range stacks {
		stackDir := filepath.Join(repo, stack)
		ensureDir(t, stackDir)
		writeFile(t, filepath.Join(stackDir, ".terraform-version"), "1.5.0")
	}

	versions, err := Detect(repo, stacks)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if versions.DefaultTerraform != "1.5.0" {
		t.Fatalf("expected default tf 1.5.0, got %q", versions.DefaultTerraform)
	}
	if len(versions.StackTerraform) != 0 {
		t.Fatalf("expected no stack overrides")
	}
}

func TestMixedVersions(t *testing.T) {
	repo := t.TempDir()
	stacks := []string{"envs/dev", "envs/prod"}
	for _, stack := range stacks {
		ensureDir(t, filepath.Join(repo, stack))
	}
	writeFile(t, filepath.Join(repo, "envs/dev", ".terraform-version"), "1.5.0")
	writeFile(t, filepath.Join(repo, "envs/prod", ".terraform-version"), "1.6.1")
	writeFile(t, filepath.Join(repo, "envs/dev", ".terragrunt-version"), "0.55.0")
	writeFile(t, filepath.Join(repo, "envs/prod", ".terragrunt-version"), "0.56.0")

	versions, err := Detect(repo, stacks)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if versions.DefaultTerraform != "" || versions.DefaultTerragrunt != "" {
		t.Fatalf("expected empty defaults")
	}
	if versions.StackTerraform["envs/dev"] != "1.5.0" || versions.StackTerraform["envs/prod"] != "1.6.1" {
		t.Fatalf("unexpected tf overrides: %#v", versions.StackTerraform)
	}
	if versions.StackTerragrunt["envs/dev"] != "0.55.0" || versions.StackTerragrunt["envs/prod"] != "0.56.0" {
		t.Fatalf("unexpected tg overrides: %#v", versions.StackTerragrunt)
	}
}

func TestNoVersionFiles(t *testing.T) {
	repo := t.TempDir()
	stacks := []string{"envs/dev"}
	ensureDir(t, filepath.Join(repo, "envs/dev"))

	versions, err := Detect(repo, stacks)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if versions.DefaultTerraform != "" || versions.DefaultTerragrunt != "" {
		t.Fatalf("expected empty defaults")
	}
	if len(versions.StackTerraform) != 0 || len(versions.StackTerragrunt) != 0 {
		t.Fatalf("expected empty stack maps")
	}
}
