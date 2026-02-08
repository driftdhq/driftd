package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/repos"
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
	provider    repos.Provider
}

type Runner interface {
	Run(ctx context.Context, repoName, repoURL, stackPath, tfVersion, tgVersion, runID string, auth transport.AuthMethod, workspacePath string) (*runner.RunResult, error)
}

func New(q *queue.Queue, r Runner, concurrency int, cfg *config.Config, provider repos.Provider) *Worker {
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
		provider:    provider,
	}
}

func (w *Worker) Start() {
	log.Printf("Starting worker %s with concurrency %d", w.id, w.concurrency)

	// Single recovery goroutine instead of per-worker recovery
	w.wg.Add(1)
	go w.recoveryLoop()

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

func (w *Worker) recoveryLoop() {
	defer w.wg.Done()

	if w.cfg == nil {
		return
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
		}

		if _, err := w.queue.RecoverStaleStackScans(w.ctx, w.cfg.Worker.ScanMaxAge); err != nil {
			log.Printf("Recovery: stale stack scans error: %v", err)
		}
		if _, err := w.queue.RecoverStaleScans(w.ctx, w.cfg.Worker.ScanMaxAge); err != nil {
			log.Printf("Recovery: stale scans error: %v", err)
		}
		if recovered, err := w.queue.RecoverOrphanedStackScans(w.ctx); err != nil {
			log.Printf("Recovery: orphaned stack scans error: %v", err)
		} else if recovered > 0 {
			log.Printf("Recovery: re-queued %d orphaned stack scans", recovered)
		}
	}
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
