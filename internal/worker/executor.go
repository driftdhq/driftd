package worker

import (
	"context"

	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/storage"
)

func (w *Worker) executePlan(ctx context.Context, sc *ScanContext) (*storage.RunResult, error) {
	cloneDepth := 1
	blockExternalDataSource := false
	if w.cfg != nil {
		cloneDepth = w.cfg.Worker.CloneDepth
		blockExternalDataSource = w.cfg.Worker.BlockExternalDataSource
	}

	return w.runner.Run(ctx, &runner.RunParams{
		ProjectName:             sc.ProjectName,
		ProjectURL:              sc.ProjectURL,
		StackPath:               sc.StackPath,
		TFVersion:               sc.TFVersion,
		TGVersion:               sc.TGVersion,
		RunID:                   sc.ScanID,
		Auth:                    sc.Auth,
		WorkspacePath:           sc.WorkspacePath,
		CloneDepth:              cloneDepth,
		BlockExternalDataSource: blockExternalDataSource,
	})
}
