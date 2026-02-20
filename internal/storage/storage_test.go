package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/driftdhq/driftd/internal/secrets"
)

func TestSaveAndGetResult(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	result := &RunResult{
		Drifted:    true,
		Added:      1,
		Changed:    2,
		Destroyed:  3,
		PlanOutput: "Plan: 1 to add, 2 to change, 3 to destroy",
		RunAt:      time.Now().Truncate(time.Second),
	}

	if err := s.SaveResult("project", "envs/prod", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	got, err := s.GetResult("project", "envs/prod")
	if err != nil {
		t.Fatalf("get result: %v", err)
	}

	if got.Drifted != result.Drifted {
		t.Errorf("drifted: got %v, want %v", got.Drifted, result.Drifted)
	}
	if got.Added != result.Added {
		t.Errorf("added: got %d, want %d", got.Added, result.Added)
	}
	if got.Changed != result.Changed {
		t.Errorf("changed: got %d, want %d", got.Changed, result.Changed)
	}
	if got.Destroyed != result.Destroyed {
		t.Errorf("destroyed: got %d, want %d", got.Destroyed, result.Destroyed)
	}
	if got.PlanOutput != result.PlanOutput {
		t.Errorf("plan output: got %q, want %q", got.PlanOutput, result.PlanOutput)
	}
	if !got.RunAt.Equal(result.RunAt) {
		t.Errorf("run at: got %v, want %v", got.RunAt, result.RunAt)
	}
}

func TestSaveAndGetResultEncryptedPlanOutput(t *testing.T) {
	dir := t.TempDir()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(secrets.EnvEncryptionKey, secrets.EncodeKey(key))

	s := New(dir)
	result := &RunResult{
		Drifted:    true,
		PlanOutput: "Plan: 1 to add, 0 to change, 0 to destroy",
		RunAt:      time.Now().Truncate(time.Second),
	}
	if err := s.SaveResult("project", "stack", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	planPath := filepath.Join(s.stackDir(s.resultsDir(), "project", "stack"), "plan.txt")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.HasPrefix(string(raw), encryptedPlanPrefix) {
		t.Fatalf("expected encrypted plan prefix, got %q", string(raw))
	}
	if strings.Contains(string(raw), result.PlanOutput) {
		t.Fatalf("expected encrypted at-rest plan output")
	}

	got, err := s.GetResult("project", "stack")
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.PlanOutput != result.PlanOutput {
		t.Fatalf("plan output mismatch: got %q want %q", got.PlanOutput, result.PlanOutput)
	}
}

func TestGetResultEncryptedPlanWithoutKeyReturnsEmptyPlan(t *testing.T) {
	dir := t.TempDir()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(secrets.EnvEncryptionKey, secrets.EncodeKey(key))

	sEncrypted := New(dir)
	result := &RunResult{
		Drifted:    true,
		PlanOutput: "sensitive plan output",
		RunAt:      time.Now(),
	}
	if err := sEncrypted.SaveResult("project", "stack", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	t.Setenv(secrets.EnvEncryptionKey, "")
	sWithoutKey := New(dir)
	got, err := sWithoutKey.GetResult("project", "stack")
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.PlanOutput != "" {
		t.Fatalf("expected empty plan output without key, got %q", got.PlanOutput)
	}
}

func TestSaveResultWithMalformedEncryptionKeyFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(secrets.EnvEncryptionKey, "not-a-valid-key")

	s := New(dir)
	result := &RunResult{
		Drifted:    true,
		PlanOutput: "sensitive plan output",
		RunAt:      time.Now(),
	}
	err := s.SaveResult("project", "stack", result)
	if err == nil {
		t.Fatalf("expected save to fail with malformed %s", secrets.EnvEncryptionKey)
	}
	if !strings.Contains(err.Error(), "plan encryption unavailable") {
		t.Fatalf("expected plan encryption error, got %v", err)
	}
}

func TestGetResultNotFound(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	_, err := s.GetResult("nonexistent", "stack")
	if err == nil {
		t.Fatal("expected error for nonexistent result")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected not exist error, got: %v", err)
	}
}

func TestSaveResultWithError(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	result := &RunResult{
		Drifted: false,
		Error:   "plan failed: access denied",
		RunAt:   time.Now(),
	}

	if err := s.SaveResult("project", "envs/dev", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	got, err := s.GetResult("project", "envs/dev")
	if err != nil {
		t.Fatalf("get result: %v", err)
	}

	if got.Error != result.Error {
		t.Errorf("error: got %q, want %q", got.Error, result.Error)
	}
}

func TestListReposEmpty(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	projects, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected empty projects, got %d", len(projects))
	}
}

func TestListReposNonexistentDir(t *testing.T) {
	s := New("/nonexistent/path")

	projects, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if projects != nil {
		t.Errorf("expected nil projects, got %v", projects)
	}
}

func TestListRepos(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Create some results
	s.SaveResult("repo1", "envs/dev", &RunResult{Drifted: true, RunAt: time.Now()})
	s.SaveResult("repo1", "envs/prod", &RunResult{Drifted: false, RunAt: time.Now()})
	s.SaveResult("repo2", "stack", &RunResult{Drifted: false, RunAt: time.Now()})

	projects, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}

	projectMap := make(map[string]ProjectStatus)
	for _, r := range projects {
		projectMap[r.Name] = r
	}

	if repo1, ok := projectMap["repo1"]; !ok {
		t.Error("missing repo1")
	} else {
		if !repo1.Drifted {
			t.Error("repo1 should be drifted (has drifted stack)")
		}
		if repo1.Stacks != 2 {
			t.Errorf("repo1 stacks: got %d, want 2", repo1.Stacks)
		}
	}

	if repo2, ok := projectMap["repo2"]; !ok {
		t.Error("missing repo2")
	} else {
		if repo2.Drifted {
			t.Error("repo2 should not be drifted")
		}
		if repo2.Stacks != 1 {
			t.Errorf("repo2 stacks: got %d, want 1", repo2.Stacks)
		}
	}
}

func TestListStacksEmpty(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	stacks, err := s.ListStacks("nonexistent")
	if err != nil {
		t.Fatalf("list stacks: %v", err)
	}
	if stacks != nil {
		t.Errorf("expected nil stacks, got %v", stacks)
	}
}

func TestListStacks(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	now := time.Now().Truncate(time.Second)
	s.SaveResult("project", "envs/dev", &RunResult{
		Drifted:   true,
		Added:     1,
		Changed:   0,
		Destroyed: 0,
		RunAt:     now,
	})
	s.SaveResult("project", "envs/prod", &RunResult{
		Drifted:   false,
		Added:     0,
		Changed:   0,
		Destroyed: 0,
		RunAt:     now,
	})

	stacks, err := s.ListStacks("project")
	if err != nil {
		t.Fatalf("list stacks: %v", err)
	}

	if len(stacks) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(stacks))
	}

	stackMap := make(map[string]StackStatus)
	for _, s := range stacks {
		stackMap[s.Path] = s
	}

	if dev, ok := stackMap["envs/dev"]; !ok {
		t.Error("missing envs/dev")
	} else {
		if !dev.Drifted {
			t.Error("envs/dev should be drifted")
		}
		if dev.Added != 1 {
			t.Errorf("envs/dev added: got %d, want 1", dev.Added)
		}
	}

	if prod, ok := stackMap["envs/prod"]; !ok {
		t.Error("missing envs/prod")
	} else {
		if prod.Drifted {
			t.Error("envs/prod should not be drifted")
		}
	}
}

func TestSafePathEncoding(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"envs/dev"},
		{"envs/prod/region/us-east-1"},
		{"stack"},
		{"path/with spaces/and-dashes"},
		{"unicode/日本語"},
	}

	for _, tt := range tests {
		encoded := safePath(tt.input)
		decoded, err := decodeSafePath(encoded)
		if err != nil {
			t.Errorf("decode %q: %v", tt.input, err)
			continue
		}
		if decoded != tt.input {
			t.Errorf("roundtrip failed: got %q, want %q", decoded, tt.input)
		}
	}
}

// Note: The legacy "__" encoding fallback in decodeSafePath is effectively broken
// because strings like "envs__prod__region" are valid base64 (the _ character is
// valid in RawURLEncoding). The fallback only triggers on actual decode errors.
// If legacy support is needed, the implementation would need to check if the
// decoded result is valid UTF-8 or use a different detection mechanism.

func TestOverwriteResult(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Save initial result
	result1 := &RunResult{
		Drifted:    true,
		Added:      5,
		PlanOutput: "initial plan",
		RunAt:      time.Now(),
	}
	if err := s.SaveResult("project", "stack", result1); err != nil {
		t.Fatalf("save result 1: %v", err)
	}

	// Overwrite with new result
	result2 := &RunResult{
		Drifted:    false,
		Added:      0,
		PlanOutput: "updated plan",
		RunAt:      time.Now(),
	}
	if err := s.SaveResult("project", "stack", result2); err != nil {
		t.Fatalf("save result 2: %v", err)
	}

	got, err := s.GetResult("project", "stack")
	if err != nil {
		t.Fatalf("get result: %v", err)
	}

	if got.Drifted != false {
		t.Error("expected drifted=false after overwrite")
	}
	if got.Added != 0 {
		t.Errorf("expected added=0, got %d", got.Added)
	}
	if got.PlanOutput != "updated plan" {
		t.Errorf("expected updated plan output")
	}
}

func TestSaveResultDoesNotLeaveTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	result := &RunResult{
		Drifted:    true,
		PlanOutput: "plan output",
		RunAt:      time.Now(),
	}
	if err := s.SaveResult("project", "stack", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	stackDir := s.stackDir(s.resultsDir(), "project", "stack")
	entries, err := os.ReadDir(stackDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".status.json.tmp-") || strings.HasPrefix(name, ".plan.txt.tmp-") {
			t.Fatalf("found leftover temp file: %s", name)
		}
	}
}

func TestGetResultMissingPlanFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Save a result
	result := &RunResult{
		Drifted:    true,
		PlanOutput: "the plan",
		RunAt:      time.Now(),
	}
	if err := s.SaveResult("project", "stack", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	// Delete the plan file
	planPath := filepath.Join(s.stackDir(s.resultsDir(), "project", "stack"), "plan.txt")
	if err := os.Remove(planPath); err != nil {
		t.Fatalf("remove plan: %v", err)
	}

	// Should still be able to get result (without plan)
	got, err := s.GetResult("project", "stack")
	if err != nil {
		t.Fatalf("get result: %v", err)
	}

	if got.PlanOutput != "" {
		t.Errorf("expected empty plan output, got %q", got.PlanOutput)
	}
	if !got.Drifted {
		t.Error("expected drifted=true")
	}
}

func TestListReposIgnoresFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Create a project with results
	s.SaveResult("project", "stack", &RunResult{RunAt: time.Now()})

	// Create a file at the data dir level (should be ignored)
	if err := os.WriteFile(filepath.Join(dir, "some-file.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	projects, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}

	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}
	if projects[0].Name != "project" {
		t.Errorf("expected project name 'project', got %q", projects[0].Name)
	}
}

func TestSaveResultRejectsInvalidProjectName(t *testing.T) {
	s := New(t.TempDir())

	err := s.SaveResult("../project", "stack", &RunResult{RunAt: time.Now()})
	if !errors.Is(err, ErrInvalidProjectName) {
		t.Fatalf("expected ErrInvalidProjectName, got %v", err)
	}
}

func TestGetResultRejectsInvalidStackPath(t *testing.T) {
	s := New(t.TempDir())

	_, err := s.GetResult("project", "../../etc/passwd")
	if !errors.Is(err, ErrInvalidStackPath) {
		t.Fatalf("expected ErrInvalidStackPath, got %v", err)
	}
}

func TestListStacksRejectsInvalidProjectName(t *testing.T) {
	s := New(t.TempDir())

	_, err := s.ListStacks("../../../tmp")
	if !errors.Is(err, ErrInvalidProjectName) {
		t.Fatalf("expected ErrInvalidProjectName, got %v", err)
	}
}
