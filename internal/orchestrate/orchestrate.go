package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/projects"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/stack"
	"github.com/driftdhq/driftd/internal/version"
	"github.com/go-git/go-git/v5"
	gitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ScanOrchestrator handles the full lifecycle of starting a scan:
// acquiring the project lock, cloning the workspace, discovering stacks,
// detecting versions, and spawning the lock renewal goroutine.
type ScanOrchestrator struct {
	cfg    *config.Config
	queue  *queue.Queue
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

const (
	cloneLockRetryEvery = 250 * time.Millisecond
	defaultCloneLockTTL = 5 * time.Minute
	minCloneRenewEvery  = 5 * time.Second
)

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

// StartScan acquires a project lock (cancelling an in-flight scan if allowed),
// clones the workspace, discovers stacks, detects versions, and spawns a
// background lock renewal goroutine. On any failure, the scan is marked failed.
func (o *ScanOrchestrator) StartScan(ctx context.Context, projectCfg *config.ProjectConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	scan, err := o.queue.StartScan(ctx, projectCfg.Name, trigger, commit, actor, 0)
	if err != nil {
		if err == queue.ErrProjectLocked && projectCfg.CancelInflightEnabled() {
			activeScan, activeErr := o.queue.GetActiveScan(ctx, projectCfg.Name)
			if activeErr == nil && activeScan != nil {
				if queue.TriggerPriority(trigger) >= queue.TriggerPriority(activeScan.Trigger) {
					scan, err = o.queue.CancelAndStartScan(ctx, activeScan.ID, projectCfg.Name, "superseded by new trigger", trigger, commit, actor, 0)
					if err == nil {
						o.queue.ClearInflightForScan(ctx, activeScan.ID)
					}
				}
			}
		}
		if err != nil {
			return nil, nil, err
		}
	}
	_ = o.queue.PublishScanEvent(ctx, projectCfg.Name, queue.ScanEvent{
		ProjectName: projectCfg.Name,
		ScanID:      scan.ID,
		Status:      scan.Status,
		StartedAt:   &scan.StartedAt,
		Total:       scan.Total,
	})

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.queue.RenewScanLock(o.ctx, scan.ID, projectCfg.Name, o.cfg.Worker.ScanMaxAge, o.cfg.Worker.RenewEvery)
	}()

	auth, err := gitauth.AuthMethod(ctx, projectCfg)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, err.Error())
		return nil, nil, err
	}

	workspacePath, commitSHA, err := o.cloneWorkspace(ctx, projectCfg, scan.ID, auth)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, err.Error())
		return nil, nil, err
	}

	if err := o.queue.SetScanWorkspace(ctx, scan.ID, workspacePath, commitSHA); err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, fmt.Sprintf("failed to set workspace: %v", err))
		return nil, nil, err
	}
	go o.cleanupWorkspaces(projectCfg.Name)

	stacks, err := stack.Discover(workspacePath, projectCfg.RootPath, projectCfg.IgnorePaths)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, err.Error())
		return nil, nil, err
	}
	if len(stacks) == 0 {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, "no stacks discovered")
		return nil, nil, fmt.Errorf("no stacks discovered")
	}
	versions, err := version.Detect(workspacePath, stacks)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, err.Error())
		return nil, nil, err
	}
	if err := o.queue.SetScanVersions(ctx, scan.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt); err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, fmt.Sprintf("failed to set versions: %v", err))
		return nil, nil, err
	}
	return scan, stacks, nil
}

// StartAndEnqueue starts a scan and enqueues all discovered stacks.
func (o *ScanOrchestrator) StartAndEnqueue(ctx context.Context, projectCfg *config.ProjectConfig, trigger, commit, actor string) (*queue.Scan, *EnqueueStacksResult, error) {
	scan, stacks, err := o.StartScan(ctx, projectCfg, trigger, commit, actor)
	if err != nil {
		return nil, nil, err
	}
	result, err := o.EnqueueStacks(ctx, scan, projectCfg, stacks, trigger, commit, actor)
	return scan, result, err
}

// StartAndEnqueueStacks starts a scan and enqueues a specific stack list.
func (o *ScanOrchestrator) StartAndEnqueueStacks(ctx context.Context, projectCfg *config.ProjectConfig, stacks []string, trigger, commit, actor string) (*queue.Scan, *EnqueueStacksResult, error) {
	scan, _, err := o.StartScan(ctx, projectCfg, trigger, commit, actor)
	if err != nil {
		return nil, nil, err
	}
	result, err := o.EnqueueStacks(ctx, scan, projectCfg, stacks, trigger, commit, actor)
	return scan, result, err
}

// EnqueueStacksResult holds the outcome of an enqueue operation.
type EnqueueStacksResult struct {
	StackIDs []string
	Errors   []string
}

// ErrNoStacksEnqueued is returned when all stacks were skipped or failed to enqueue.
var ErrNoStacksEnqueued = errors.New("no stacks enqueued")

// EnqueueStacks sets the scan total, batch-enqueues stack scans, and adjusts
// scan counters for any skips or failures. Returns ErrNoStacksEnqueued if
// nothing was successfully enqueued (scan is auto-cancelled in that case).
func (o *ScanOrchestrator) EnqueueStacks(ctx context.Context, scan *queue.Scan, projectCfg *config.ProjectConfig, stacks []string, trigger, commit, actor string) (*EnqueueStacksResult, error) {
	maxRetries := 0
	if o.cfg != nil && o.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	if err := o.queue.SetScanTotal(ctx, scan.ID, len(stacks)); err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, fmt.Sprintf("failed to set scan total: %v", err))
		return nil, err
	}

	// Build StackScan objects
	batch := make([]*queue.StackScan, len(stacks))
	for i, stackPath := range stacks {
		batch[i] = &queue.StackScan{
			ScanID:      scan.ID,
			ProjectName: projectCfg.Name,
			ProjectURL:  projectCfg.URL,
			StackPath:   stackPath,
			MaxRetries:  maxRetries,
			Trigger:     trigger,
			Commit:      commit,
			Actor:       actor,
		}
	}

	batchResult, err := o.queue.EnqueueBatch(ctx, batch)
	if err != nil {
		_ = o.queue.FailScan(ctx, scan.ID, projectCfg.Name, fmt.Sprintf("batch enqueue failed: %v", err))
		return nil, err
	}

	result := &EnqueueStacksResult{
		Errors: batchResult.Errors,
	}
	for _, ss := range batchResult.Enqueued {
		result.StackIDs = append(result.StackIDs, ss.ID)
	}

	// Adjust scan counters for skips and errors in one atomic call
	skipCount := batchResult.Skipped
	errCount := len(batchResult.Errors)
	if skipCount > 0 || errCount > 0 {
		var deltas []any
		if skipCount > 0 {
			deltas = append(deltas, "queued", -skipCount, "total", -skipCount)
		}
		if errCount > 0 {
			deltas = append(deltas, "queued", -errCount, "failed", errCount, "errored", errCount)
		}
		_ = o.queue.AdjustScanCounters(ctx, scan.ID, projectCfg.Name, deltas...)
	}

	if len(result.StackIDs) == 0 {
		_ = o.queue.CancelScan(ctx, scan.ID, projectCfg.Name, "all stacks inflight")
		return result, ErrNoStacksEnqueued
	}

	return result, nil
}

func (o *ScanOrchestrator) cloneWorkspace(ctx context.Context, projectCfg *config.ProjectConfig, scanID string, auth transport.AuthMethod) (workspacePath, commitSHA string, err error) {
	cloneURL := projectCfg.EffectiveCloneURL()
	if strings.TrimSpace(cloneURL) == "" {
		return "", "", fmt.Errorf("project clone URL is empty")
	}
	if scanID == "" {
		scanID = fmt.Sprintf("%s:%d", projectCfg.Name, time.Now().UnixNano())
	}

	urlHash := hashCloneURL(cloneURL)
	mirrorPath := filepath.Join(o.cfg.DataDir, "workspaces", "_shared", urlHash, "mirror.git")
	scanWorkspace := filepath.Join(o.cfg.DataDir, "workspaces", "scans", projectCfg.Name, scanID, "project")

	var releaseCloneLock func() error
	if o.queue != nil {
		owner := scanID
		lockTTL := o.cfg.Worker.LockTTL
		if lockTTL <= 0 {
			lockTTL = defaultCloneLockTTL
		}
		if err := o.acquireCloneLock(ctx, urlHash, owner, lockTTL); err != nil {
			return "", "", err
		}

		lockCtx, cancelLockCtx := context.WithCancel(ctx)
		ctx = lockCtx
		stopRenewal := o.startCloneLockRenewal(lockCtx, urlHash, owner, lockTTL, cancelLockCtx)
		releaseCloneLock = func() error {
			defer cancelLockCtx()
			if err := stopRenewal(); err != nil {
				return err
			}
			if err := o.queue.ReleaseCloneLock(context.Background(), urlHash, owner); err != nil {
				return fmt.Errorf("release clone lock: %w", err)
			}
			return nil
		}
		defer func() {
			if releaseCloneLock == nil {
				return
			}
			if releaseErr := releaseCloneLock(); releaseErr != nil {
				// Preserve the first meaningful error for caller handling.
				if err == nil {
					err = releaseErr
				}
			}
		}()
	}

	mirrorRepo, err := o.openOrCreateMirror(ctx, mirrorPath, cloneURL, auth)
	if err != nil {
		return "", "", err
	}
	if err := o.fetchMirror(ctx, mirrorRepo, auth); err != nil {
		return "", "", err
	}

	hash, err := resolveTargetRef(mirrorRepo, projectCfg.Branch)
	if err != nil {
		return "", "", err
	}

	if err := o.checkoutScanWorkspace(ctx, mirrorPath, scanWorkspace, hash); err != nil {
		return "", "", err
	}
	return scanWorkspace, hash.String(), nil
}

func (o *ScanOrchestrator) acquireCloneLock(ctx context.Context, urlHash, owner string, ttl time.Duration) error {
	ticker := time.NewTicker(cloneLockRetryEvery)
	defer ticker.Stop()

	for {
		acquired, err := o.queue.AcquireCloneLock(ctx, urlHash, owner, ttl)
		if err != nil {
			return fmt.Errorf("acquire clone lock: %w", err)
		}
		if acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (o *ScanOrchestrator) openOrCreateMirror(ctx context.Context, mirrorPath, cloneURL string, auth transport.AuthMethod) (*git.Repository, error) {
	if err := os.MkdirAll(filepath.Dir(mirrorPath), 0755); err != nil {
		return nil, err
	}

	project, err := git.PlainOpen(mirrorPath)
	if err == nil {
		return project, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, err
	}

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return git.PlainCloneContext(cloneCtx, mirrorPath, true, &git.CloneOptions{
		URL:        cloneURL,
		Auth:       auth,
		NoCheckout: true,
	})
}

func (o *ScanOrchestrator) fetchMirror(ctx context.Context, project *git.Repository, auth transport.AuthMethod) error {
	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err := project.FetchContext(fetchCtx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Tags:       git.NoTags,
		Force:      true,
		Prune:      true,
		RefSpecs:   []gitcfg.RefSpec{"+refs/heads/*:refs/heads/*"},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return err
	}
	return nil
}

func (o *ScanOrchestrator) checkoutScanWorkspace(ctx context.Context, mirrorPath, scanWorkspace string, hash plumbing.Hash) error {
	if err := os.MkdirAll(filepath.Dir(scanWorkspace), 0755); err != nil {
		return err
	}
	_ = os.RemoveAll(scanWorkspace)

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	project, err := git.PlainCloneContext(cloneCtx, scanWorkspace, false, &git.CloneOptions{
		URL:        mirrorPath,
		NoCheckout: true,
	})
	if err != nil {
		return err
	}

	wt, err := project.Worktree()
	if err != nil {
		return err
	}
	return wt.Checkout(&git.CheckoutOptions{
		Hash:  hash,
		Force: true,
	})
}

func (o *ScanOrchestrator) cloneLockRenewEvery(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return minCloneRenewEvery
	}
	if o != nil && o.cfg != nil && o.cfg.Worker.RenewEvery > 0 && o.cfg.Worker.RenewEvery < ttl {
		return o.cfg.Worker.RenewEvery
	}
	renewEvery := ttl / 3
	if renewEvery < minCloneRenewEvery {
		renewEvery = minCloneRenewEvery
	}
	maxRenewEvery := ttl / 2
	if maxRenewEvery < minCloneRenewEvery {
		maxRenewEvery = minCloneRenewEvery
	}
	if renewEvery > maxRenewEvery {
		renewEvery = maxRenewEvery
	}
	return renewEvery
}

func (o *ScanOrchestrator) startCloneLockRenewal(ctx context.Context, urlHash, owner string, lockTTL time.Duration, cancel context.CancelFunc) func() error {
	renewEvery := o.cloneLockRenewEvery(lockTTL)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			if err := o.queue.RenewCloneLock(ctx, urlHash, owner, lockTTL); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				select {
				case errCh <- fmt.Errorf("renew clone lock: %w", err):
				default:
				}
				cancel()
				return
			}
		}
	}()

	return func() error {
		cancel()
		wg.Wait()
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
}

func resolveTargetRef(project *git.Repository, branch string) (plumbing.Hash, error) {
	if branch != "" {
		for _, refName := range []plumbing.ReferenceName{
			plumbing.NewBranchReferenceName(branch),
			plumbing.NewRemoteReferenceName("origin", branch),
		} {
			if ref, err := project.Reference(refName, true); err == nil {
				return ref.Hash(), nil
			}
		}
	}

	for _, name := range []plumbing.ReferenceName{
		plumbing.HEAD,
		plumbing.NewBranchReferenceName("main"),
		plumbing.NewBranchReferenceName("master"),
		plumbing.NewRemoteReferenceName("origin", "HEAD"),
		plumbing.NewRemoteReferenceName("origin", "main"),
		plumbing.NewRemoteReferenceName("origin", "master"),
	} {
		if ref, err := project.Reference(name, true); err == nil {
			return ref.Hash(), nil
		}
	}

	head, err := project.Head()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return head.Hash(), nil
}

func hashCloneURL(cloneURL string) string {
	identity := strings.TrimSpace(cloneURL)
	if canonical, ok := projects.CanonicalURL(identity); ok {
		identity = canonical
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func (o *ScanOrchestrator) cleanupWorkspaces(projectName string) {
	retention := o.cfg.Workspace.Retention
	if retention <= 0 {
		return
	}

	base := filepath.Join(o.cfg.DataDir, "workspaces", "scans", projectName)
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

	if len(items) <= retention {
		return
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].mod.After(items[j].mod)
	})

	toDelete := items[retention:]
	for _, it := range toDelete {
		scan, err := o.queue.GetScan(context.Background(), it.id)
		if err == nil && scan != nil && scan.Status == queue.ScanStatusRunning {
			continue
		}
		_ = os.RemoveAll(it.path)
	}
}
