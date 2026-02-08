package worker

import (
	"context"
	"log"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type jobContext struct {
	scan          *queue.Scan
	tfVersion     string
	tgVersion     string
	workspacePath string
	auth          transport.AuthMethod
}

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

	ctxData, canceled := w.loadScanContext(job)
	if canceled {
		return
	}

	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Minute)
	defer cancel()
	if job.ScanID != "" {
		go w.watchScanCancel(ctx, cancel, job.ScanID)
	}

	if err := w.resolveAuth(ctx, job, ctxData); err != nil {
		log.Printf("Stack scan %s failed (git auth): %v", job.ID, err)
		if failErr := w.queue.Fail(w.ctx, job, err.Error()); failErr != nil {
			log.Printf("Failed to mark stack scan %s as failed: %v", job.ID, failErr)
		}
		return
	}

	result, err := w.runner.Run(ctx, job.RepoName, job.RepoURL, job.StackPath, ctxData.tfVersion, ctxData.tgVersion, ctxData.auth, ctxData.workspacePath)
	if ctxData.workspacePath != "" && w.cfg != nil && w.cfg.Workspace.CleanupAfterPlanEnabled() {
		if err := runner.CleanupWorkspaceArtifacts(ctxData.workspacePath); err != nil {
			log.Printf("Failed to cleanup workspace artifacts for %s: %v", ctxData.workspacePath, err)
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
		CommitSHA:  scan.CommitSHA,
		StartedAt:  &scan.StartedAt,
		EndedAt:    scanEndedAt(scan),
		Status:     scan.Status,
		Completed:  scan.Completed,
		Failed:     scan.Failed,
		Total:      scan.Total,
		DriftedCnt: scan.Drifted,
	})
}

func scanEndedAt(scan *queue.Scan) *time.Time {
	if scan == nil {
		return nil
	}
	if scan.EndedAt.IsZero() {
		return nil
	}
	return &scan.EndedAt
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
