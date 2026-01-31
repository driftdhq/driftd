package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/cbrown132/driftd/internal/queue"
	"github.com/cbrown132/driftd/internal/runner"
)

type Worker struct {
	id          string
	queue       *queue.Queue
	runner      *runner.Runner
	concurrency int
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
}

func New(q *queue.Queue, r *runner.Runner, concurrency int) *Worker {
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

		w.processJob(job)
	}
}

func (w *Worker) processJob(job *queue.Job) {
	log.Printf("Processing job %s: %s/%s", job.ID, job.RepoName, job.StackPath)

	// Create context with timeout for the plan execution
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Minute)
	defer cancel()

	result, err := w.runner.Run(ctx, job.RepoName, job.RepoURL, job.StackPath)

	if err != nil {
		log.Printf("Job %s failed (internal error): %v", job.ID, err)
		if failErr := w.queue.Fail(w.ctx, job, err.Error()); failErr != nil {
			log.Printf("Failed to mark job %s as failed: %v", job.ID, failErr)
		}
		return
	}

	if result.Error != "" {
		log.Printf("Job %s failed (plan error): %s", job.ID, result.Error)
		if failErr := w.queue.Fail(w.ctx, job, result.Error); failErr != nil {
			log.Printf("Failed to mark job %s as failed: %v", job.ID, failErr)
		}
		return
	}

	log.Printf("Job %s completed: drifted=%v added=%d changed=%d destroyed=%d",
		job.ID, result.Drifted, result.Added, result.Changed, result.Destroyed)

	if completeErr := w.queue.Complete(w.ctx, job, result.Drifted); completeErr != nil {
		log.Printf("Failed to mark job %s as completed: %v", job.ID, completeErr)
	}
}
