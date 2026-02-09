package worker

import (
	"log"
	"path/filepath"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/storage"
)

func (w *Worker) reportResult(job *queue.StackScan, sc *ScanContext, result *storage.RunResult, err error) {
	if sc != nil && sc.WorkspacePath != "" && w.cfg != nil && w.cfg.Workspace.CleanupAfterPlanEnabled() {
		stackDir := filepath.Join(sc.WorkspacePath, job.StackPath)
		defer func() {
			if err := runner.CleanupWorkspaceArtifacts(stackDir); err != nil {
				log.Printf("Failed to cleanup workspace artifacts for %s: %v", stackDir, err)
			}
		}()
	}

	if err != nil {
		log.Printf("Stack scan %s failed (internal error): %v", job.ID, err)
		w.failStack(job, sc, err.Error())
		return
	}

	if result != nil && result.Error != "" {
		log.Printf("Stack scan %s failed (plan error): %s", job.ID, result.Error)
		w.failStack(job, sc, result.Error)
		return
	}

	if result == nil {
		w.failStack(job, sc, "plan failed")
		return
	}

	log.Printf("Stack scan %s completed: drifted=%v added=%d changed=%d destroyed=%d",
		job.ID, result.Drifted, result.Added, result.Changed, result.Destroyed)

	if completeErr := w.queue.Complete(w.ctx, job, result.Drifted); completeErr != nil {
		log.Printf("Failed to mark stack scan %s as completed: %v", job.ID, completeErr)
	}
	w.publishStackCompletion(job, sc, result)
}

func (w *Worker) failStack(job *queue.StackScan, sc *ScanContext, errMsg string) {
	if sc == nil {
		sc = &ScanContext{}
	}
	if failErr := w.queue.Fail(w.ctx, job, errMsg); failErr != nil {
		log.Printf("Failed to mark stack scan %s as failed: %v", job.ID, failErr)
	}
	w.publishStackFailure(job, sc, errMsg)
}

func (w *Worker) publishStackFailure(job *queue.StackScan, sc *ScanContext, errMsg string) {
	now := time.Now()
	_ = w.queue.PublishStackEvent(w.ctx, job.RepoName, queue.StackEvent{
		RepoName:  job.RepoName,
		ScanID:    job.ScanID,
		StackPath: job.StackPath,
		Status:    "failed",
		Error:     errMsg,
		RunAt:     &now,
	})
}

func (w *Worker) publishStackCompletion(job *queue.StackScan, sc *ScanContext, result *storage.RunResult) {
	now := time.Now()
	drifted := result.Drifted
	_ = w.queue.PublishStackEvent(w.ctx, job.RepoName, queue.StackEvent{
		RepoName:  job.RepoName,
		ScanID:    job.ScanID,
		StackPath: job.StackPath,
		Status:    "completed",
		Drifted:   &drifted,
		RunAt:     &now,
	})
}
