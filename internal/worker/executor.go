package worker

import (
	"context"

	"github.com/driftdhq/driftd/internal/runner"
)

func (w *Worker) executePlan(ctx context.Context, sc *ScanContext) (*runner.RunResult, error) {
	return w.runner.Run(ctx, sc.RepoName, sc.RepoURL, sc.StackPath, sc.TFVersion, sc.TGVersion, sc.Auth, sc.WorkspacePath)
}
