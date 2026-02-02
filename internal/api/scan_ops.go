package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

func (s *Server) startScanWithCancel(ctx context.Context, repoCfg *config.RepoConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	scan, err := s.queue.StartScan(ctx, repoCfg.Name, trigger, commit, actor, 0)
	if err != nil {
		activeScan, activeErr := s.queue.GetActiveScan(ctx, repoCfg.Name)
		if repoCfg.CancelInflightEnabled() && activeErr == nil && activeScan != nil {
			activePriority := queue.TriggerPriority(activeScan.Trigger)
			if queue.TriggerPriority(trigger) >= activePriority {
				_ = s.queue.CancelScan(ctx, activeScan.ID, repoCfg.Name, "superseded by new trigger")
				scan, err = s.queue.StartScan(ctx, repoCfg.Name, trigger, commit, actor, 0)
			}
		}
		if err != nil {
			return nil, nil, err
		}
	}

	// Use Background context because renewal must continue independent of the HTTP request.
	// The goroutine exits when scan status changes to completed/failed/canceled.
	go s.queue.RenewScanLock(context.Background(), scan.ID, repoCfg.Name, s.cfg.Worker.ScanMaxAge, s.cfg.Worker.RenewEvery)

	auth, err := gitauth.AuthMethod(ctx, repoCfg)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}

	workspacePath, commitSHA, err := s.cloneWorkspace(ctx, repoCfg, scan.ID, auth)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}

	if err := s.queue.SetScanWorkspace(ctx, scan.ID, workspacePath, commitSHA); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set workspace: %v", err))
		return nil, nil, err
	}
	go s.cleanupWorkspaces(repoCfg.Name, scan.ID)

	stacks, err := stack.Discover(workspacePath, repoCfg.IgnorePaths)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}
	if len(stacks) == 0 {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, "no stacks discovered")
		return nil, nil, fmt.Errorf("no stacks discovered")
	}
	versions, err := version.Detect(workspacePath, stacks)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}
	if err := s.queue.SetScanVersions(ctx, scan.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set versions: %v", err))
		return nil, nil, err
	}
	if err := s.queue.SetScanTotal(ctx, scan.ID, len(stacks)); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set scan total: %v", err))
		return nil, nil, err
	}

	return scan, stacks, nil
}

func (s *Server) cloneWorkspace(ctx context.Context, repoCfg *config.RepoConfig, scanID string, auth transport.AuthMethod) (string, string, error) {
	base := filepath.Join(s.cfg.DataDir, "workspaces", repoCfg.Name, scanID, "repo")
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

func (s *Server) cleanupWorkspaces(repoName, keepScanID string) {
	retention := s.cfg.Workspace.Retention
	if retention <= 0 {
		return
	}

	base := filepath.Join(s.cfg.DataDir, "workspaces", repoName)
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
		scan, err := s.queue.GetScan(context.Background(), it.id)
		if err == nil && scan != nil && scan.Status == queue.ScanStatusRunning {
			continue
		}
		// Note: There's a small race window where scan status could change between
		// the check and RemoveAll. This is acceptable because workers copy the
		// workspace to a temp directory before processing, so deletion during
		// processing won't affect the running stack scan.
		_ = os.RemoveAll(it.path)
	}
}
