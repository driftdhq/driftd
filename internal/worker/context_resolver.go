package worker

import (
	"context"
	"errors"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
)

var errScanCanceled = errors.New("scan canceled")

func (w *Worker) resolveScanContext(ctx context.Context, job *queue.StackScan) (*ScanContext, error) {
	sc := &ScanContext{
		ProjectName: job.ProjectName,
		ProjectURL:  job.ProjectURL,
		StackPath:   job.StackPath,
		ScanID:      job.ScanID,
	}

	if job.ScanID != "" {
		scan, err := w.queue.GetScan(w.ctx, job.ScanID)
		if err == nil && scan != nil {
			sc.Scan = scan
			if scan.Status == queue.ScanStatusCanceled {
				_ = w.queue.CancelStackScan(w.ctx, job, "scan canceled")
				return nil, errScanCanceled
			}
			sc.CommitSHA = scan.CommitSHA
			sc.WorkspacePath = scan.WorkspacePath

			if v, ok := scan.StackTFVersions[job.StackPath]; ok {
				sc.TFVersion = v
			} else {
				sc.TFVersion = scan.TerraformVersion
			}
			if v, ok := scan.StackTGVersions[job.StackPath]; ok {
				sc.TGVersion = v
			} else {
				sc.TGVersion = scan.TerragruntVersion
			}
		}
	}

	if err := w.resolveAuth(ctx, job, sc); err != nil {
		return nil, err
	}

	return sc, nil
}

func (w *Worker) resolveAuth(ctx context.Context, job *queue.StackScan, sc *ScanContext) error {
	if w.cfg == nil || sc.WorkspacePath != "" {
		return nil
	}

	var projectCfg *config.ProjectConfig
	if w.provider != nil {
		if resolved, err := w.provider.Get(job.ProjectName); err == nil {
			projectCfg = resolved
		}
	} else {
		projectCfg = w.cfg.GetProject(job.ProjectName)
	}
	if projectCfg == nil {
		return nil
	}

	authMethod, authErr := gitauth.AuthMethod(ctx, projectCfg)
	if authErr != nil {
		return authErr
	}
	sc.Auth = authMethod
	return nil
}
