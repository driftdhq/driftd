package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/stack"
	"github.com/driftdhq/driftd/internal/version"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ScanOrchestrator handles the full lifecycle of starting a scan:
// acquiring the repo lock, cloning the workspace, discovering stacks,
// detecting versions, and spawning the lock renewal goroutine.
type ScanOrchestrator struct {
	cfg    *config.Config
	queue  *queue.Queue
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(cfg *config.Config, q *queue.Queue) *ScanOrchestrator {
	ctx, cancel := context.WithCancel(context.Background())
	return &ScanOrchestrator{
		cfg:    cfg,
		queue:  q,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Stop cancels all in-flight lock renewal goroutines and waits for them to exit.
func (o *ScanOrchestrator) Stop() {
	o.cancel()
	o.wg.Wait()
}

// StartScan acquires a repo lock (cancelling an in-flight scan if allowed),
// clones the workspace, discovers stacks, detects versions, and spawns a
// background lock renewal goroutine. On any failure, the scan is marked failed.
func (o *ScanOrchestrator) StartScan(ctx context.Context, repoCfg *config.RepoConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	scan, err := o.queue.StartScan(ctx, repoCfg.Name, trigger, commit, actor, 0)
	if err != nil {
		if err == queue.ErrRepoLocked && repoCfg.CancelInflightEnabled() {
			activeScan, activeErr := o.queue.GetActiveScan(ctx, repoCfg.Name)
			if activeErr == nil && activeScan != nil {
				if queue.TriggerPriority(trigger) >= queue.TriggerPriority(activeScan.Trigger) {
					scan, err = o.queue.CancelAndStartScan(ctx, activeScan.ID, repoCfg.Name, "superseded by new trigger", trigger, commit, actor, 0)
				}
			}
		}
		if err != nil {
			return nil, nil, err
		}
	}
	_ = o.queue.PublishEvent(ctx, repoCfg.Name, queue.RepoEvent{
		Type:      "scan_update",
		RepoName:  repoCfg.Name,
		ScanID:    scan.ID,
		Status:    scan.Status,
		StartedAt: &scan.StartedAt,
		Total:     scan.Total,
	})

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.queue.RenewScanLock(o.ctx, scan.ID, repoCfg.Name, o.cfg.Worker.ScanMaxAge, o.cfg.Worker.RenewEvery)
	}()

	auth, err := gitauth.AuthMethod(ctx, repoCfg)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}

	workspacePath, commitSHA, err := o.cloneWorkspace(ctx, repoCfg, scan.ID, auth)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}

	if err := o.queue.SetScanWorkspace(ctx, scan.ID, workspacePath, commitSHA); err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set workspace: %v", err))
		return nil, nil, err
	}
	go o.cleanupWorkspaces(repoCfg.Name, scan.ID)

	stacks, err := stack.Discover(workspacePath, repoCfg.IgnorePaths)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}
	if len(stacks) == 0 {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, "no stacks discovered")
		return nil, nil, fmt.Errorf("no stacks discovered")
	}
	versions, err := version.Detect(workspacePath, stacks)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}
	if err := o.queue.SetScanVersions(ctx, scan.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt); err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set versions: %v", err))
		return nil, nil, err
	}
	if err := o.queue.SetScanTotal(ctx, scan.ID, len(stacks)); err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set scan total: %v", err))
		return nil, nil, err
	}

	return scan, stacks, nil
}

func (o *ScanOrchestrator) cloneWorkspace(ctx context.Context, repoCfg *config.RepoConfig, scanID string, auth transport.AuthMethod) (string, string, error) {
	base := filepath.Join(o.cfg.DataDir, "workspaces", repoCfg.Name, scanID, "repo")
	if err := os.MkdirAll(filepath.Dir(base), 0755); err != nil {
		return base, "", err
	}

	cloneOpts := &git.CloneOptions{
		URL:   repoCfg.URL,
		Depth: 1,
		Auth:  auth,
	}
	if repoCfg.Branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(repoCfg.Branch)
		cloneOpts.SingleBranch = true
	}
	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	repo, err := git.PlainCloneContext(cloneCtx, base, false, cloneOpts)
	if err != nil {
		return base, "", err
	}

	head, err := repo.Head()
	if err != nil {
		return base, "", err
	}
	return base, head.Hash().String(), nil
}

func (o *ScanOrchestrator) cleanupWorkspaces(repoName, keepScanID string) {
	retention := o.cfg.Workspace.Retention
	if retention <= 0 {
		return
	}

	base := filepath.Join(o.cfg.DataDir, "workspaces", repoName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}

	type item struct {
		id   string
		path string
		mod  time.Time
	}
	var items []item
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if id == keepScanID {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, item{
			id:   id,
			path: filepath.Join(base, id),
			mod:  info.ModTime(),
		})
	}

	if len(items) <= retention-1 {
		return
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].mod.After(items[j].mod)
	})

	toDelete := items[retention-1:]
	for _, it := range toDelete {
		scan, err := o.queue.GetScan(context.Background(), it.id)
		if err == nil && scan != nil && scan.Status == queue.ScanStatusRunning {
			continue
		}
		_ = os.RemoveAll(it.path)
	}
}
