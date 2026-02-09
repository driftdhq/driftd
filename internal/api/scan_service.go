package api

import (
	"context"
	"fmt"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
)

func (s *Server) startScanWithCancel(ctx context.Context, repoCfg *config.RepoConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	return s.orchestrator.StartScan(ctx, repoCfg, trigger, commit, actor)
}

func (s *Server) enqueueStacks(ctx context.Context, scan *queue.Scan, repoCfg *config.RepoConfig, stacks []string, trigger, commit, actor string) ([]string, []string, error) {
	maxRetries := 0
	if s.cfg != nil && s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	var stackIDs []string
	var errs []string

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
				continue
			}
			_ = s.queue.MarkScanEnqueueFailed(ctx, scan.ID)
			errs = append(errs, fmt.Sprintf("%s: %s", stackPath, s.sanitizeErrorMessage(err.Error())))
			continue
		}
		stackIDs = append(stackIDs, stackScan.ID)
	}

	if err := s.queue.SetScanTotal(ctx, scan.ID, len(stackIDs)); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set scan total: %v", err))
		return stackIDs, errs, err
	}
	if len(stackIDs) == 0 {
		err := fmt.Errorf("no stacks enqueued (all inflight)")
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return stackIDs, errs, err
	}
	return stackIDs, errs, nil
}
