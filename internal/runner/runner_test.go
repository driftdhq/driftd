package runner

import (
	"os"
	"os/exec"
	"path/filepath"
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

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
