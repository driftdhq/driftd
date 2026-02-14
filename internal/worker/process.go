package worker

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
)

func (w *Worker) processStackScan(job *queue.StackScan) {
	log.Printf("Processing stack scan %s: %s/%s", job.ID, job.ProjectName, job.StackPath)

	now := time.Now()
	_ = w.queue.PublishStackEvent(w.ctx, job.ProjectName, queue.StackEvent{
		ProjectName: job.ProjectName,
		ScanID:      job.ScanID,
		StackPath:   job.StackPath,
		Status:      "running",
		RunAt:       &now,
	})

	sc, err := w.resolveScanContext(w.ctx, job)
	if err != nil {
		if errors.Is(err, errScanCanceled) {
			return
		}
		w.failStack(job, nil, err.Error())
		return
	}

	timeout := 30 * time.Minute
	if w.cfg != nil && w.cfg.Worker.StackTimeout > 0 {
		timeout = w.cfg.Worker.StackTimeout
	}
	ctx, cancel := context.WithTimeout(w.ctx, timeout)
	defer cancel()
	if job.ScanID != "" {
		go w.watchScanCancel(ctx, cancel, job.ScanID)
	}

	result, execErr := w.executePlan(ctx, sc)
	w.reportResult(job, sc, result, execErr)
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
