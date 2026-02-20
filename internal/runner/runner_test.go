package runner

import (
	"fmt"
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

func TestPlanOnlyProxyBlocksApply(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "terraform")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}

	wrapperPath := filepath.Join(dir, "terraform.planonly")
	if err := os.WriteFile(planOnlyTargetPath(wrapperPath), []byte(realBin+"\n"), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	stdoutFile := filepath.Join(dir, "stdout.txt")
	stderrFile := filepath.Join(dir, "stderr.txt")
	stdout, err := os.OpenFile(stdoutFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open stdout file: %v", err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(stderrFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open stderr file: %v", err)
	}
	defer stderr.Close()

	code := runPlanOnlyProxy(wrapperPath, []string{"apply"}, stdout, stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	content, err := os.ReadFile(stderrFile)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if !strings.Contains(string(content), "terraform subcommand disabled: apply") {
		t.Fatalf("expected disabled subcommand message, got: %s", string(content))
	}
}

func TestPlanOnlyProxyAllowsPlanAndForwardsArgs(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "terraform")
	argsFile := filepath.Join(dir, "args.txt")
	fakeTerraform := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", argsFile)
	if err := os.WriteFile(realBin, []byte(fakeTerraform), 0755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}

	wrapperPath := filepath.Join(dir, "terraform.planonly")
	if err := os.WriteFile(planOnlyTargetPath(wrapperPath), []byte(realBin+"\n"), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	stdout, err := os.OpenFile(filepath.Join(dir, "stdout.txt"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open stdout file: %v", err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(dir, "stderr.txt"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open stderr file: %v", err)
	}
	defer stderr.Close()

	code := runPlanOnlyProxy(wrapperPath, []string{"-chdir=foo", "plan", "-input=false"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("expected plan to succeed, got exit code %d", code)
	}

	content, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read forwarded args: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(content)), "\n")
	want := []string{"-chdir=foo", "plan", "-input=false"}
	if len(got) != len(want) {
		t.Fatalf("unexpected arg count: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d mismatch: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestEnsurePlanOnlyWrapperCreatesLinkAndTarget(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "terraform")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}

	wrapperPath, err := ensurePlanOnlyWrapper(dir, realBin)
	if err != nil {
		t.Fatalf("ensure wrapper: %v", err)
	}
	if !planOnlyWrapperValid(wrapperPath, realBin) {
		t.Fatalf("expected valid wrapper at %s", wrapperPath)
	}

	target, err := readPlanOnlyTarget(wrapperPath)
	if err != nil {
		t.Fatalf("read wrapper target: %v", err)
	}
	if target != realBin {
		t.Fatalf("target mismatch: got %q want %q", target, realBin)
	}
}

func TestIsBlockedTerraformSubcommand(t *testing.T) {
	tests := []struct {
		name       string
		subcommand string
		blocked    bool
	}{
		{name: "apply", subcommand: "apply", blocked: true},
		{name: "destroy", subcommand: "destroy", blocked: true},
		{name: "import", subcommand: "import", blocked: true},
		{name: "taint", subcommand: "taint", blocked: true},
		{name: "untaint", subcommand: "untaint", blocked: true},
		{name: "state", subcommand: "state", blocked: true},
		{name: "console", subcommand: "console", blocked: true},
		{name: "login", subcommand: "login", blocked: true},
		{name: "logout", subcommand: "logout", blocked: true},
		{name: "plan", subcommand: "plan", blocked: false},
		{name: "show", subcommand: "show", blocked: false},
		{name: "empty", subcommand: "", blocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBlockedTerraformSubcommand(tt.subcommand); got != tt.blocked {
				t.Fatalf("isBlockedTerraformSubcommand(%q) = %v, want %v", tt.subcommand, got, tt.blocked)
			}
		})
	}
}

func TestMaybeRunPlanOnlyProxy(t *testing.T) {
	handled, code := MaybeRunPlanOnlyProxy("/tmp/terraform", []string{"plan"})
	if handled {
		t.Fatalf("expected non-wrapper argv0 to be ignored")
	}
	if code != 0 {
		t.Fatalf("expected code 0 for ignored argv0, got %d", code)
	}

	dir := t.TempDir()
	realBin := filepath.Join(dir, "terraform")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}
	wrapperPath := filepath.Join(dir, "terraform.planonly")
	if err := os.WriteFile(planOnlyTargetPath(wrapperPath), []byte(realBin+"\n"), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	handled, code = MaybeRunPlanOnlyProxy(wrapperPath, []string{"plan"})
	if !handled {
		t.Fatalf("expected wrapper argv0 to be handled")
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
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
	// Save and restore environment variables modified by this test.
	origHome := os.Getenv("HOME")
	origPath := os.Getenv("PATH")
	origTmpdir := os.Getenv("TMPDIR")

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
	if err := os.Setenv("AWS_ACCESS_KEY_ID", "AKIA0123456789ABCDEF"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	if err := os.Setenv("AWS_SECRET_ACCESS_KEY", "secret"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	if err := os.Setenv("HTTP_PROXY", "http://proxy.local:3128"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	defer func() {
		_ = os.Unsetenv("TF_TEST_VAR")
		_ = os.Unsetenv("TERRAGRUNT_TEST_VAR")
		_ = os.Unsetenv("AWS_ACCESS_KEY_ID")
		_ = os.Unsetenv("AWS_SECRET_ACCESS_KEY")
		_ = os.Unsetenv("HTTP_PROXY")
		_ = os.Unsetenv("SHOULD_NOT_LEAK")
		_ = os.Setenv("HOME", origHome)
		_ = os.Setenv("PATH", origPath)
		_ = os.Setenv("TMPDIR", origTmpdir)
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
	if envMap["AWS_ACCESS_KEY_ID"] == "" || envMap["AWS_SECRET_ACCESS_KEY"] == "" {
		t.Fatalf("expected AWS credentials to be present in env")
	}
	if envMap["HTTP_PROXY"] == "" {
		t.Fatalf("expected HTTP_PROXY to be present in env")
	}
	if envMap["HOME"] == "" || envMap["PATH"] == "" || envMap["TMPDIR"] == "" {
		t.Fatalf("expected HOME, PATH, TMPDIR to be present")
	}
}

func TestCleanTerragruntOutput(t *testing.T) {
	input := "INFO   terraform.planonly: Initializing...\nSTDOUT terraform.planonly: Plan: 1 to add\nWARN   Something else\n"
	got := cleanTerragruntOutput("terragrunt", input)
	if strings.Contains(got, "terraform.planonly:") {
		t.Fatalf("expected terragrunt prefixes to be stripped, got: %q", got)
	}
	if strings.Contains(got, "Something else") {
		t.Fatalf("expected non-terraform terragrunt logs to be removed, got: %q", got)
	}
	if !strings.Contains(got, "Plan: 1 to add") {
		t.Fatalf("expected terraform output to remain, got: %q", got)
	}
}

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
