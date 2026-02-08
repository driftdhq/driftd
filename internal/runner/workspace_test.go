package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/driftdhq/driftd/internal/storage"
)

func TestPrepareRepoRoot_SharedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	os.MkdirAll(filepath.Join(workspace, "envs/prod"), 0755)
	os.WriteFile(filepath.Join(workspace, "envs/prod/main.tf"), []byte("# prod"), 0644)

	r := &Runner{}
	root, cleanup, err := r.prepareRepoRoot(context.Background(), "", workspace, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup for shared workspace")
	}
	if root != workspace {
		t.Fatalf("expected root=%s, got %s", workspace, root)
	}

	// Verify files are accessible directly (no copy)
	data, err := os.ReadFile(filepath.Join(root, "envs/prod/main.tf"))
	if err != nil {
		t.Fatalf("expected to read file directly from workspace: %v", err)
	}
	if string(data) != "# prod" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestPrepareRepoRoot_NoWorkspace_ClonesFresh(t *testing.T) {
	// Without a workspace or valid repo URL, the clone should fail.
	// This verifies the clone path is taken (not the shared workspace path).
	r := &Runner{}
	_, cleanup, err := r.prepareRepoRoot(context.Background(), "file:///nonexistent", "", nil)
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("expected error for invalid repo URL")
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup on error")
	}
}

func TestPrepareRepoRoot_SharedWorkspace_NoTempDirCreated(t *testing.T) {
	workspace := t.TempDir()
	os.WriteFile(filepath.Join(workspace, "main.tf"), []byte("# root"), 0644)

	r := &Runner{}

	// Count temp dirs before
	tmpEntries, _ := os.ReadDir(os.TempDir())
	driftdCountBefore := 0
	for _, e := range tmpEntries {
		if len(e.Name()) > 6 && e.Name()[:6] == "driftd" {
			driftdCountBefore++
		}
	}

	root, cleanup, err := r.prepareRepoRoot(context.Background(), "", workspace, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup")
	}

	// Count after — no new driftd temp dirs should exist
	tmpEntries, _ = os.ReadDir(os.TempDir())
	driftdCountAfter := 0
	for _, e := range tmpEntries {
		if len(e.Name()) > 6 && e.Name()[:6] == "driftd" {
			driftdCountAfter++
		}
	}
	if driftdCountAfter > driftdCountBefore {
		t.Fatalf("shared workspace created temp dirs: before=%d after=%d", driftdCountBefore, driftdCountAfter)
	}
	_ = root
}

func TestRunWithSharedWorkspace_UsesDirectly(t *testing.T) {
	workspace := t.TempDir()
	stackDir := filepath.Join(workspace, "envs/prod")
	os.MkdirAll(stackDir, 0755)

	// Write a main.tf so the stack path is found
	os.WriteFile(filepath.Join(stackDir, "main.tf"), []byte(`resource "null_resource" "a" {}`), 0644)

	// Create a marker file so we can verify no copy happened
	marker := filepath.Join(workspace, ".marker")
	os.WriteFile(marker, []byte("original"), 0644)

	store := storage.New(t.TempDir())
	r := New(store)

	// Run will fail at planStack (no real terraform binary) but that's fine —
	// we're testing that it reaches the stack path directly without copying.
	result, _ := r.Run(context.Background(), "test-repo", "", "envs/prod", "", "", "", nil, workspace)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// The marker file should still exist in the workspace (not cleaned up),
	// confirming no temp copy was made and deleted.
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Fatal("workspace was unexpectedly modified or deleted")
	}
}

func TestRunWithSharedWorkspace_ConcurrentStacks(t *testing.T) {
	workspace := t.TempDir()

	// Create two stack directories
	for _, stack := range []string{"envs/prod", "envs/dev"} {
		stackDir := filepath.Join(workspace, stack)
		os.MkdirAll(stackDir, 0755)
		os.WriteFile(filepath.Join(stackDir, "main.tf"), []byte(`resource "null_resource" "x" {}`), 0644)
	}

	store := storage.New(t.TempDir())
	r := New(store)

	var wg sync.WaitGroup
	results := make([]*RunResult, 2)

	for i, stack := range []string{"envs/prod", "envs/dev"} {
		wg.Add(1)
		go func(idx int, sp string) {
			defer wg.Done()
			res, _ := r.Run(context.Background(), "test-repo", "", sp, "", "", "", nil, workspace)
			results[idx] = res
		}(i, stack)
	}
	wg.Wait()

	for i, res := range results {
		if res == nil {
			t.Fatalf("stack %d: expected non-nil result", i)
		}
		// Both should reach the plan stage (and fail due to no terraform binary),
		// not fail with "stack path not found" or workspace errors.
		if res.Error == "stack path not found: envs/prod" || res.Error == "stack path not found: envs/dev" {
			t.Fatalf("stack %d: workspace interference — got %q", i, res.Error)
		}
	}

	// Workspace should still be intact after concurrent runs
	for _, stack := range []string{"envs/prod", "envs/dev"} {
		tf := filepath.Join(workspace, stack, "main.tf")
		if _, err := os.Stat(tf); os.IsNotExist(err) {
			t.Fatalf("workspace file %s was deleted", tf)
		}
	}
}

func TestRunWithSharedWorkspace_InvalidStackPath(t *testing.T) {
	workspace := t.TempDir()
	store := storage.New(t.TempDir())
	r := New(store)

	result, _ := r.Run(context.Background(), "test-repo", "", "nonexistent/stack", "", "", "", nil, workspace)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Error != "stack path not found: nonexistent/stack" {
		t.Fatalf("expected stack-not-found error, got: %q", result.Error)
	}
}

func TestRunWithSharedWorkspace_UnsafeStackPath(t *testing.T) {
	workspace := t.TempDir()
	store := storage.New(t.TempDir())
	r := New(store)

	result, _ := r.Run(context.Background(), "test-repo", "", "../etc/passwd", "", "", "", nil, workspace)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Error != "invalid stack path" {
		t.Fatalf("expected invalid-stack-path error, got: %q", result.Error)
	}
}
