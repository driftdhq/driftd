package orchestrate

import (
	"context"
	"errors"
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
	projectDir := t.TempDir()
	dataDir := t.TempDir()
	initGitRepo(t, projectDir)

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

	projectCfg := &config.ProjectConfig{
		Name: "project",
		URL:  "file://" + projectDir,
	}

	scan, stacks, err := orch.StartScan(context.Background(), projectCfg, "manual", "", "")
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
	expectedWorkspace := filepath.Join(dataDir, "workspaces", "scans", projectCfg.Name, scan.ID, "project")
	if state.WorkspacePath != expectedWorkspace {
		t.Fatalf("expected workspace path %s, got %s", expectedWorkspace, state.WorkspacePath)
	}
}

func TestCloneWorkspaceFetchesUpdates(t *testing.T) {
	projectDir := t.TempDir()
	dataDir := t.TempDir()
	project := initGitRepo(t, projectDir)

	cfg := &config.Config{
		DataDir: dataDir,
		Worker: config.WorkerConfig{
			LockTTL:    time.Minute,
			ScanMaxAge: time.Hour,
			RenewEvery: time.Minute,
		},
	}
	orch := New(cfg, nil)

	projectCfg := &config.ProjectConfig{
		Name: "project",
		URL:  "file://" + projectDir,
	}

	workspace, commit1, err := orch.cloneWorkspace(context.Background(), projectCfg, "scan-a", nil)
	if err != nil {
		t.Fatalf("clone workspace: %v", err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "second.tf"), []byte(`resource "null_resource" "second" {}`), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	wt, err := project.Worktree()
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

	workspace2, commit2, err := orch.cloneWorkspace(context.Background(), projectCfg, "scan-b", nil)
	if err != nil {
		t.Fatalf("clone workspace (update): %v", err)
	}
	if workspace == workspace2 {
		t.Fatalf("expected immutable per-scan workspaces, got same path %s", workspace)
	}
	if commit1 == commit2 {
		t.Fatalf("expected new commit hash after fetch")
	}
	if _, err := os.Stat(filepath.Join(workspace2, "second.tf")); err != nil {
		t.Fatalf("expected updated file in workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "second.tf")); err == nil {
		t.Fatalf("expected first workspace snapshot to remain immutable")
	}
}

func initGitRepo(t *testing.T, dir string) *git.Repository {
	t.Helper()

	project, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init project: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`resource "null_resource" "test" {}`), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	wt, err := project.Worktree()
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
	return project
}

func TestCloneLockRenewalKeepsLockOwned(t *testing.T) {
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
		Worker: config.WorkerConfig{
			RenewEvery: 40 * time.Millisecond,
		},
	}
	orch := New(cfg, q)

	const (
		urlHash = "renew-lock"
		ownerA  = "owner-a"
		ownerB  = "owner-b"
	)
	lockTTL := 150 * time.Millisecond
	acquired, err := q.AcquireCloneLock(context.Background(), urlHash, ownerA, lockTTL)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if !acquired {
		t.Fatalf("expected initial acquire to succeed")
	}

	lockCtx, cancel := context.WithCancel(context.Background())
	stopRenewal := orch.startCloneLockRenewal(lockCtx, urlHash, ownerA, lockTTL, cancel)

	time.Sleep(450 * time.Millisecond)
	acquired, err = q.AcquireCloneLock(context.Background(), urlHash, ownerB, lockTTL)
	if err != nil {
		t.Fatalf("acquire competing lock: %v", err)
	}
	if acquired {
		t.Fatalf("expected lock to still be held by renewal")
	}

	if err := stopRenewal(); err != nil {
		t.Fatalf("stop renewal: %v", err)
	}
	if err := q.ReleaseCloneLock(context.Background(), urlHash, ownerA); err != nil {
		t.Fatalf("release owner lock: %v", err)
	}
	acquired, err = q.AcquireCloneLock(context.Background(), urlHash, ownerB, lockTTL)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if !acquired {
		t.Fatalf("expected lock to be acquirable after release")
	}
}

func TestCloneLockRenewalFailsWhenOwnerMismatch(t *testing.T) {
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
		Worker: config.WorkerConfig{
			RenewEvery: 25 * time.Millisecond,
		},
	}
	orch := New(cfg, q)

	const urlHash = "renew-owner-mismatch"
	lockTTL := 200 * time.Millisecond
	acquired, err := q.AcquireCloneLock(context.Background(), urlHash, "owner-a", lockTTL)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if !acquired {
		t.Fatalf("expected initial acquire to succeed")
	}

	lockCtx, cancel := context.WithCancel(context.Background())
	stopRenewal := orch.startCloneLockRenewal(lockCtx, urlHash, "owner-b", lockTTL, cancel)
	time.Sleep(120 * time.Millisecond)

	renewErr := stopRenewal()
	if renewErr == nil {
		t.Fatalf("expected owner-mismatch renewal error")
	}
	if !errors.Is(renewErr, queue.ErrCloneLockNotOwned) {
		t.Fatalf("expected ErrCloneLockNotOwned, got %v", renewErr)
	}
}
