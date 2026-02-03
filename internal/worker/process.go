package worker

import (
	"context"
	"log"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

func (w *Worker) processStackScan(job *queue.StackScan) {
	log.Printf("Processing stack scan %s: %s/%s", job.ID, job.RepoName, job.StackPath)

	now := time.Now()
	_ = w.queue.PublishEvent(w.ctx, job.RepoName, queue.RepoEvent{
		Type:      "stack_update",
		RepoName:  job.RepoName,
		ScanID:    job.ScanID,
		StackPath: job.StackPath,
		Status:    "running",
		RunAt:     &now,
	})

	var tfVersion, tgVersion string
	var auth transport.AuthMethod
	var scanID string
	var workspacePath string
	if job.ScanID != "" {
		scanID = job.ScanID
		if scan, err := w.queue.GetScan(w.ctx, job.ScanID); err == nil && scan != nil {
			if scan.Status == queue.ScanStatusCanceled {
				_ = w.queue.CancelStackScan(w.ctx, job, "scan canceled")
				return
			}
			if v, ok := scan.StackTFVersions[job.StackPath]; ok {
				tfVersion = v
			} else {
				tfVersion = scan.TerraformVersion
			}
			if v, ok := scan.StackTGVersions[job.StackPath]; ok {
				tgVersion = v
			} else {
				tgVersion = scan.TerragruntVersion
			}
			workspacePath = scan.WorkspacePath
		}
	}

	// Create context with timeout for the plan execution
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Minute)
	defer cancel()
	if scanID != "" {
		go w.watchScanCancel(ctx, cancel, scanID)
	}

	if w.cfg != nil {
		var repoCfg *config.RepoConfig
		if w.provider != nil {
			if resolved, err := w.provider.Get(job.RepoName); err == nil {
				repoCfg = resolved
			}
		} else {
			repoCfg = w.cfg.GetRepo(job.RepoName)
		}
		if repoCfg != nil {
			if workspacePath == "" {
				authMethod, authErr := gitauth.AuthMethod(ctx, repoCfg)
				if authErr != nil {
					log.Printf("Stack scan %s failed (git auth): %v", job.ID, authErr)
					if failErr := w.queue.Fail(w.ctx, job, authErr.Error()); failErr != nil {
						log.Printf("Failed to mark stack scan %s as failed: %v", job.ID, failErr)
					}
					return
				}
				auth = authMethod
			}
		}
	}

	result, err := w.runner.Run(ctx, job.RepoName, job.RepoURL, job.StackPath, tfVersion, tgVersion, auth, workspacePath)
	if workspacePath != "" && w.cfg != nil && w.cfg.Workspace.CleanupAfterPlanEnabled() {
		if err := runner.CleanupWorkspaceArtifacts(workspacePath); err != nil {
			log.Printf("Failed to cleanup workspace artifacts for %s: %v", workspacePath, err)
		}
	}

	if err != nil {
		log.Printf("Stack scan %s failed (internal error): %v", job.ID, err)
		if failErr := w.queue.Fail(w.ctx, job, err.Error()); failErr != nil {
			log.Printf("Failed to mark stack scan %s as failed: %v", job.ID, failErr)
		}
		w.publishStackFailure(job, err.Error())
		return
	}

	if result.Error != "" {
		log.Printf("Stack scan %s failed (plan error): %s", job.ID, result.Error)
		if failErr := w.queue.Fail(w.ctx, job, result.Error); failErr != nil {
			log.Printf("Failed to mark stack scan %s as failed: %v", job.ID, failErr)
		}
		w.publishStackFailure(job, result.Error)
		return
	}

	log.Printf("Stack scan %s completed: drifted=%v added=%d changed=%d destroyed=%d",
		job.ID, result.Drifted, result.Added, result.Changed, result.Destroyed)

	if completeErr := w.queue.Complete(w.ctx, job, result.Drifted); completeErr != nil {
		log.Printf("Failed to mark stack scan %s as completed: %v", job.ID, completeErr)
	}
	w.publishStackCompletion(job, result)
}

func (w *Worker) publishStackFailure(job *queue.StackScan, errMsg string) {
	now := time.Now()
	_ = w.queue.PublishEvent(w.ctx, job.RepoName, queue.RepoEvent{
		Type:      "stack_update",
		RepoName:  job.RepoName,
		ScanID:    job.ScanID,
		StackPath: job.StackPath,
		Status:    "failed",
		Error:     errMsg,
		RunAt:     &now,
	})
	w.publishScanUpdate(job)
}

func (w *Worker) publishStackCompletion(job *queue.StackScan, result *runner.RunResult) {
	now := time.Now()
	drifted := result.Drifted
	_ = w.queue.PublishEvent(w.ctx, job.RepoName, queue.RepoEvent{
		Type:      "stack_update",
		RepoName:  job.RepoName,
		ScanID:    job.ScanID,
		StackPath: job.StackPath,
		Status:    "completed",
		Drifted:   &drifted,
		RunAt:     &now,
	})
	w.publishScanUpdate(job)
}

func (w *Worker) publishScanUpdate(job *queue.StackScan) {
	if job.ScanID == "" {
		return
	}
	scan, err := w.queue.GetScan(w.ctx, job.ScanID)
	if err != nil || scan == nil {
		return
	}
	_ = w.queue.PublishEvent(w.ctx, job.RepoName, queue.RepoEvent{
		Type:       "scan_update",
		RepoName:   job.RepoName,
		ScanID:     scan.ID,
		Status:     scan.Status,
		Completed:  scan.Completed,
		Failed:     scan.Failed,
		Total:      scan.Total,
		DriftedCnt: scan.Drifted,
	})
}

func (w *Worker) watchScanCancel(ctx context.Context, cancel context.CancelFunc, scanID string) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		scan, err := w.queue.GetScan(ctx, scanID)
		if err != nil || scan == nil {
			continue
		}
		if scan.Status == queue.ScanStatusCanceled {
			cancel()
			return
		}
	}
}
