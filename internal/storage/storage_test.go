package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	if err := s.SaveResult("repo", "envs/prod", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	got, err := s.GetResult("repo", "envs/prod")
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

	if err := s.SaveResult("repo", "envs/dev", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	got, err := s.GetResult("repo", "envs/dev")
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

	repos, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected empty repos, got %d", len(repos))
	}
}

func TestListReposNonexistentDir(t *testing.T) {
	s := New("/nonexistent/path")

	repos, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}
	if repos != nil {
		t.Errorf("expected nil repos, got %v", repos)
	}
}

func TestListRepos(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Create some results
	s.SaveResult("repo1", "envs/dev", &RunResult{Drifted: true, RunAt: time.Now()})
	s.SaveResult("repo1", "envs/prod", &RunResult{Drifted: false, RunAt: time.Now()})
	s.SaveResult("repo2", "stack", &RunResult{Drifted: false, RunAt: time.Now()})

	repos, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}

	repoMap := make(map[string]RepoStatus)
	for _, r := range repos {
		repoMap[r.Name] = r
	}

	if repo1, ok := repoMap["repo1"]; !ok {
		t.Error("missing repo1")
	} else {
		if !repo1.Drifted {
			t.Error("repo1 should be drifted (has drifted stack)")
		}
		if repo1.Stacks != 2 {
			t.Errorf("repo1 stacks: got %d, want 2", repo1.Stacks)
		}
	}

	if repo2, ok := repoMap["repo2"]; !ok {
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
	s.SaveResult("repo", "envs/dev", &RunResult{
		Drifted:   true,
		Added:     1,
		Changed:   0,
		Destroyed: 0,
		RunAt:     now,
	})
	s.SaveResult("repo", "envs/prod", &RunResult{
		Drifted:   false,
		Added:     0,
		Changed:   0,
		Destroyed: 0,
		RunAt:     now,
	})

	stacks, err := s.ListStacks("repo")
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
	if err := s.SaveResult("repo", "stack", result1); err != nil {
		t.Fatalf("save result 1: %v", err)
	}

	// Overwrite with new result
	result2 := &RunResult{
		Drifted:    false,
		Added:      0,
		PlanOutput: "updated plan",
		RunAt:      time.Now(),
	}
	if err := s.SaveResult("repo", "stack", result2); err != nil {
		t.Fatalf("save result 2: %v", err)
	}

	got, err := s.GetResult("repo", "stack")
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
	if err := s.SaveResult("repo", "stack", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	stackDir := s.stackDir("repo", "stack")
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
	if err := s.SaveResult("repo", "stack", result); err != nil {
		t.Fatalf("save result: %v", err)
	}

	// Delete the plan file
	planPath := filepath.Join(s.stackDir("repo", "stack"), "plan.txt")
	if err := os.Remove(planPath); err != nil {
		t.Fatalf("remove plan: %v", err)
	}

	// Should still be able to get result (without plan)
	got, err := s.GetResult("repo", "stack")
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

	// Create a repo with results
	s.SaveResult("repo", "stack", &RunResult{RunAt: time.Now()})

	// Create a file at the data dir level (should be ignored)
	if err := os.WriteFile(filepath.Join(dir, "some-file.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	repos, err := s.ListRepos()
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}

	if len(repos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].Name != "repo" {
		t.Errorf("expected repo name 'repo', got %q", repos[0].Name)
	}
}
