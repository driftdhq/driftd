package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestStartScanCreatesScanAndStacks(t *testing.T) {
	repoDir := t.TempDir()
	dataDir := t.TempDir()
	initGitRepo(t, repoDir)

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	q, err := queue.New(mr.Addr(), "", 0, time.Minute)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	defer q.Close()

	cfg := &config.Config{
		DataDir: dataDir,
		Worker: config.WorkerConfig{
			LockTTL:    time.Minute,
			ScanMaxAge: time.Hour,
			RenewEvery: time.Minute,
		},
	}

	orch := New(cfg, q)
	defer orch.Stop()

	repoCfg := &config.RepoConfig{
		Name: "repo",
		URL:  "file://" + repoDir,
	}

	scan, stacks, err := orch.StartScan(context.Background(), repoCfg, "manual", "", "")
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}
	if scan == nil {
		t.Fatalf("expected scan")
	}
	if len(stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(stacks))
	}

	state, err := q.GetScan(context.Background(), scan.ID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	if state.Total != 0 || state.Queued != 0 {
		t.Fatalf("expected total=0 queued=0, got total=%d queued=%d", state.Total, state.Queued)
	}
	if state.WorkspacePath == "" {
		t.Fatalf("expected workspace path set")
	}
	if _, err := os.Stat(state.WorkspacePath); err != nil {
		t.Fatalf("workspace missing: %v", err)
	}
	expectedWorkspace := filepath.Join(dataDir, "workspaces", repoCfg.Name, "repo")
	if state.WorkspacePath != expectedWorkspace {
		t.Fatalf("expected workspace path %s, got %s", expectedWorkspace, state.WorkspacePath)
	}
}

func TestCloneWorkspaceFetchesUpdates(t *testing.T) {
	repoDir := t.TempDir()
	dataDir := t.TempDir()
	repo := initGitRepo(t, repoDir)

	cfg := &config.Config{
		DataDir: dataDir,
		Worker: config.WorkerConfig{
			LockTTL:    time.Minute,
			ScanMaxAge: time.Hour,
			RenewEvery: time.Minute,
		},
	}
	orch := New(cfg, nil)

	repoCfg := &config.RepoConfig{
		Name: "repo",
		URL:  "file://" + repoDir,
	}

	workspace, commit1, err := orch.cloneWorkspace(context.Background(), repoCfg, nil)
	if err != nil {
		t.Fatalf("clone workspace: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "second.tf"), []byte(`resource "null_resource" "second" {}`), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("second.tf"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("second", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "tester",
			Email: "tester@example.com",
			When:  time.Now(),
		},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	workspace2, commit2, err := orch.cloneWorkspace(context.Background(), repoCfg, nil)
	if err != nil {
		t.Fatalf("clone workspace (update): %v", err)
	}
	if workspace != workspace2 {
		t.Fatalf("expected same workspace, got %s and %s", workspace, workspace2)
	}
	if commit1 == commit2 {
		t.Fatalf("expected new commit hash after fetch")
	}
	if _, err := os.Stat(filepath.Join(workspace2, "second.tf")); err != nil {
		t.Fatalf("expected updated file in workspace: %v", err)
	}
}

func initGitRepo(t *testing.T, dir string) *git.Repository {
	t.Helper()

	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`resource "null_resource" "test" {}`), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("main.tf"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "tester",
			Email: "tester@example.com",
			When:  time.Now(),
		},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return repo
}
