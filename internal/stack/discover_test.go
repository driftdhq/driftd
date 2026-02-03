package stack

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDiscoverStacks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "envs/prod/main.tf"))
	writeFile(t, filepath.Join(dir, "envs/dev/terragrunt.hcl"))
	writeFile(t, filepath.Join(dir, "modules/shared/main.tf"))

	stacks, err := Discover(dir, []string{"**/modules/**"})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	sort.Strings(stacks)
	want := []string{"envs/dev", "envs/prod"}
	if len(stacks) != len(want) {
		t.Fatalf("expected %d stacks, got %d (%v)", len(want), len(stacks), stacks)
	}
	for i := range want {
		if stacks[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, stacks)
		}
	}
}

func TestDiscoverRespectsDefaultIgnore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".terraform/ignored.tf"))
	writeFile(t, filepath.Join(dir, ".terragrunt-cache/ignored.hcl"))
	writeFile(t, filepath.Join(dir, "app/main.tf"))

	stacks, err := Discover(dir, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(stacks) != 1 || stacks[0] != "app" {
		t.Fatalf("expected only app stack, got %v", stacks)
	}
}

func TestDiscoverPrefersTerragruntWhenRootConfigPresent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "terragrunt.hcl"))
	writeFile(t, filepath.Join(dir, "envs/prod/terragrunt.hcl"))
	writeFile(t, filepath.Join(dir, "modules/app/main.tf"))
	writeFile(t, filepath.Join(dir, "modules/db/main.tf"))

	stacks, err := Discover(dir, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	sort.Strings(stacks)
	want := []string{"envs/prod"}
	if len(stacks) != len(want) {
		t.Fatalf("expected %d stacks, got %d (%v)", len(want), len(stacks), stacks)
	}
	for i := range want {
		if stacks[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, stacks)
		}
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
