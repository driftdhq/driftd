package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePlanSummary(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		added     int
		changed   int
		destroyed int
	}{
		{
			name:      "plan summary",
			output:    "Plan: 1 to add, 2 to change, 3 to destroy",
			added:     1,
			changed:   2,
			destroyed: 3,
		},
		{
			name:      "no changes",
			output:    "No changes. Your infrastructure matches the configuration.",
			added:     0,
			changed:   0,
			destroyed: 0,
		},
		{
			name:      "no differences",
			output:    "There are no differences between your configuration and the real world infrastructure.",
			added:     0,
			changed:   0,
			destroyed: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, changed, destroyed := parsePlanSummary(tt.output)
			if added != tt.added || changed != tt.changed || destroyed != tt.destroyed {
				t.Fatalf("got %d/%d/%d, want %d/%d/%d", added, changed, destroyed, tt.added, tt.changed, tt.destroyed)
			}
		})
	}
}

func TestPlanOnlyWrapperBlocksApply(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "terraform")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}

	wrapper, err := ensurePlanOnlyWrapper(dir, realBin)
	if err != nil {
		t.Fatalf("ensure wrapper: %v", err)
	}

	cmd := execCommand(wrapper, "apply")
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected apply to be blocked")
	}
}

func TestPlanOnlyWrapperAllowsPlan(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "terraform")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}

	wrapper, err := ensurePlanOnlyWrapper(dir, realBin)
	if err != nil {
		t.Fatalf("ensure wrapper: %v", err)
	}

	cmd := execCommand(wrapper, "plan")
	if err := cmd.Run(); err != nil {
		t.Fatalf("expected plan to succeed, got %v", err)
	}
}

func TestDetectToolTerraform(t *testing.T) {
	dir := t.TempDir()
	if got := detectTool(dir); got != "terraform" {
		t.Fatalf("expected terraform, got %s", got)
	}
}

func TestDetectToolTerragrunt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terragrunt.hcl"), []byte(""), 0644); err != nil {
		t.Fatalf("write terragrunt.hcl: %v", err)
	}
	if got := detectTool(dir); got != "terragrunt" {
		t.Fatalf("expected terragrunt, got %s", got)
	}
}

func TestFilteredEnv(t *testing.T) {
	if err := os.Setenv("TF_TEST_VAR", "1"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	if err := os.Setenv("TERRAGRUNT_TEST_VAR", "2"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	if err := os.Setenv("SHOULD_NOT_LEAK", "nope"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	if err := os.Setenv("HOME", "/tmp"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	if err := os.Setenv("PATH", "/bin"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	if err := os.Setenv("TMPDIR", "/tmpdir"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	defer func() {
		_ = os.Unsetenv("TF_TEST_VAR")
		_ = os.Unsetenv("TERRAGRUNT_TEST_VAR")
		_ = os.Unsetenv("SHOULD_NOT_LEAK")
	}()

	env := filteredEnv()
	envMap := map[string]string{}
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envMap[parts[0]] = parts[1]
	}

	if envMap["TF_TEST_VAR"] != "1" {
		t.Fatalf("expected TF_TEST_VAR in env")
	}
	if envMap["TERRAGRUNT_TEST_VAR"] != "2" {
		t.Fatalf("expected TERRAGRUNT_TEST_VAR in env")
	}
	if _, ok := envMap["SHOULD_NOT_LEAK"]; ok {
		t.Fatalf("unexpected SHOULD_NOT_LEAK in env")
	}
	if envMap["HOME"] == "" || envMap["PATH"] == "" || envMap["TMPDIR"] == "" {
		t.Fatalf("expected HOME, PATH, TMPDIR to be present")
	}
}

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
