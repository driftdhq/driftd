package worker

import (
	"context"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
)

func (w *Worker) loadScanContext(job *queue.StackScan) (*jobContext, bool) {
	ctx := &jobContext{}
	if job.ScanID == "" {
		return ctx, false
	}

	scan, err := w.queue.GetScan(w.ctx, job.ScanID)
	if err != nil || scan == nil {
		return ctx, false
	}
	ctx.scan = scan

	if scan.Status == queue.ScanStatusCanceled {
		_ = w.queue.CancelStackScan(w.ctx, job, "scan canceled")
		return nil, true
	}

	_ = w.queue.PublishEvent(w.ctx, job.RepoName, queue.RepoEvent{
		Type:       "scan_update",
		RepoName:   job.RepoName,
		ScanID:     scan.ID,
		Status:     scan.Status,
		CommitSHA:  scan.CommitSHA,
		StartedAt:  &scan.StartedAt,
		EndedAt:    scanEndedAt(scan),
		Completed:  scan.Completed,
		Failed:     scan.Failed,
		Total:      scan.Total,
		DriftedCnt: scan.Drifted,
	})

	if v, ok := scan.StackTFVersions[job.StackPath]; ok {
		ctx.tfVersion = v
	} else {
		ctx.tfVersion = scan.TerraformVersion
	}
	if v, ok := scan.StackTGVersions[job.StackPath]; ok {
		ctx.tgVersion = v
	} else {
		ctx.tgVersion = scan.TerragruntVersion
	}
	ctx.workspacePath = scan.WorkspacePath

	return ctx, false
}

func (w *Worker) resolveAuth(ctx context.Context, job *queue.StackScan, ctxData *jobContext) error {
	if w.cfg == nil {
		return nil
	}

	var repoCfg *config.RepoConfig
	if w.provider != nil {
		if resolved, err := w.provider.Get(job.RepoName); err == nil {
			repoCfg = resolved
		}
	} else {
		repoCfg = w.cfg.GetRepo(job.RepoName)
	}
	if repoCfg == nil || ctxData.workspacePath != "" {
		return nil
	}

	authMethod, authErr := gitauth.AuthMethod(ctx, repoCfg)
	if authErr != nil {
		return authErr
	}
	ctxData.auth = authMethod
	return nil
}
