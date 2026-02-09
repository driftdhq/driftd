package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
)

func (s *Server) startScanWithCancel(ctx context.Context, repoCfg *config.RepoConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	return s.orchestrator.StartScan(ctx, repoCfg, trigger, commit, actor)
}

var errNoStacksEnqueued = errors.New("no stacks enqueued")

func (s *Server) enqueueStacks(ctx context.Context, scan *queue.Scan, repoCfg *config.RepoConfig, stacks []string, trigger, commit, actor string) ([]string, []string, error) {
	maxRetries := 0
	if s.cfg != nil && s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	var stackIDs []string
	var errs []string

	if err := s.queue.SetScanTotal(ctx, scan.ID, len(stacks)); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set scan total: %v", err))
		return stackIDs, errs, err
	}
	defer func() {
		_ = s.queue.ReleaseScanLock(context.Background(), repoCfg.Name, scan.ID)
	}()

	for _, stackPath := range stacks {
		stackScan := &queue.StackScan{
			ScanID:     scan.ID,
			RepoName:   repoCfg.Name,
			RepoURL:    repoCfg.URL,
			StackPath:  stackPath,
			MaxRetries: maxRetries,
			Trigger:    trigger,
			Commit:     commit,
			Actor:      actor,
		}

		if err := s.queue.Enqueue(ctx, stackScan); err != nil {
			if err == queue.ErrStackScanInflight {
				_ = s.queue.MarkScanEnqueueSkipped(ctx, scan.ID)
				continue
			}
			_ = s.queue.MarkScanEnqueueFailed(ctx, scan.ID)
			errs = append(errs, fmt.Sprintf("%s: %s", stackPath, s.sanitizeErrorMessage(err.Error())))
			continue
		}
		stackIDs = append(stackIDs, stackScan.ID)
	}

	if len(stackIDs) == 0 {
		_ = s.queue.CancelScan(ctx, scan.ID, repoCfg.Name, "all stacks inflight")
		return stackIDs, errs, errNoStacksEnqueued
	}

	_ = s.queue.ReleaseScanLock(ctx, repoCfg.Name, scan.ID)
	return stackIDs, errs, nil
}
