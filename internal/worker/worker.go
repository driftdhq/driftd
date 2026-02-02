package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type Worker struct {
	id          string
	queue       *queue.Queue
	runner      Runner
	concurrency int
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	cfg         *config.Config
}

type Runner interface {
	Run(ctx context.Context, repoName, repoURL, stackPath, tfVersion, tgVersion string, auth transport.AuthMethod, workspacePath string) (*runner.RunResult, error)
}

func New(q *queue.Queue, r Runner, concurrency int, cfg *config.Config) *Worker {
	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	ctx, cancel := context.WithCancel(context.Background())

	return &Worker{
		id:          workerID,
		queue:       q,
		runner:      r,
		concurrency: concurrency,
		ctx:         ctx,
		cancel:      cancel,
		cfg:         cfg,
	}
}

func (w *Worker) Start() {
	log.Printf("Starting worker %s with concurrency %d", w.id, w.concurrency)

	for i := 0; i < w.concurrency; i++ {
		w.wg.Add(1)
		go w.processLoop(i)
	}
}

func (w *Worker) Stop() {
	log.Printf("Stopping worker %s", w.id)
	w.cancel()
	w.wg.Wait()
	log.Printf("Worker %s stopped", w.id)
}

func (w *Worker) processLoop(workerNum int) {
	defer w.wg.Done()

	workerID := fmt.Sprintf("%s-%d", w.id, workerNum)
	log.Printf("Worker goroutine %s started", workerID)

	for {
		select {
		case <-w.ctx.Done():
			log.Printf("Worker goroutine %s shutting down", workerID)
			return
		default:
		}

		// Create a context with timeout for dequeue
		dequeueCtx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
		job, err := w.queue.Dequeue(dequeueCtx, workerID)
		cancel()

		if err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				continue
			}
			log.Printf("Worker %s dequeue error: %v", workerID, err)
			time.Sleep(5 * time.Second)
			continue
		}

		w.processStackScan(job)
	}
}

func (w *Worker) processStackScan(job *queue.StackScan) {
	log.Printf("Processing stack scan %s: %s/%s", job.ID, job.RepoName, job.StackPath)

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
		if repoCfg := w.cfg.GetRepo(job.RepoName); repoCfg != nil {
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
		return
	}

	if result.Error != "" {
		log.Printf("Stack scan %s failed (plan error): %s", job.ID, result.Error)
		if failErr := w.queue.Fail(w.ctx, job, result.Error); failErr != nil {
			log.Printf("Failed to mark stack scan %s as failed: %v", job.ID, failErr)
		}
		return
	}

	log.Printf("Stack scan %s completed: drifted=%v added=%d changed=%d destroyed=%d",
		job.ID, result.Drifted, result.Added, result.Changed, result.Destroyed)

	if completeErr := w.queue.Complete(w.ctx, job, result.Drifted); completeErr != nil {
		log.Printf("Failed to mark stack scan %s as completed: %v", job.ID, completeErr)
	}
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
