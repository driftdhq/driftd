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
	Run(ctx context.Context, repoName, repoURL, stackPath, tfVersion, tgVersion string, auth transport.AuthMethod, workspacePath string) (*runner.RunResult, error)
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
