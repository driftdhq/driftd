package metrics

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
	"github.com/prometheus/client_golang/prometheus"
)

const repoEventsPattern = "driftd:events:*"

var (
	registerOnce sync.Once

	activeScans *prometheus.GaugeVec

	scansCompleted *prometheus.CounterVec
	scansFailed    *prometheus.CounterVec
	scansCanceled  *prometheus.CounterVec

	stackCompleted *prometheus.CounterVec
	stackFailed    *prometheus.CounterVec
	stackDrifted   *prometheus.CounterVec

	stackDuration *prometheus.HistogramVec
)

type eventState struct {
	mu          sync.Mutex
	scanStatus  map[string]string
	stackStatus map[string]string
	stackStart  map[string]time.Time
}

func Register(q *queue.Queue) {
	registerOnce.Do(func() {
		if q == nil {
			return
		}

		activeScans = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "driftd",
			Name:      "active_scans",
			Help:      "Number of active scans per repository.",
		}, []string{"repo"})

		scansCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "scans_completed_total",
			Help:      "Number of scans completed successfully.",
		}, []string{"repo"})
		scansFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "scans_failed_total",
			Help:      "Number of scans that failed.",
		}, []string{"repo"})
		scansCanceled = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "scans_canceled_total",
			Help:      "Number of scans that were canceled.",
		}, []string{"repo"})

		stackCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "stack_scans_completed_total",
			Help:      "Number of stack scans completed successfully.",
		}, []string{"repo"})
		stackFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "stack_scans_failed_total",
			Help:      "Number of stack scans that failed.",
		}, []string{"repo"})
		stackDrifted = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "stack_scans_drifted_total",
			Help:      "Number of stack scans that detected drift.",
		}, []string{"repo"})

		stackDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "driftd",
			Name:      "stack_scan_duration_seconds",
			Help:      "Duration of stack scans in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"repo"})

		prometheus.MustRegister(
			activeScans,
			scansCompleted,
			scansFailed,
			scansCanceled,
			stackCompleted,
			stackFailed,
			stackDrifted,
			stackDuration,
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Namespace: "driftd",
				Name:      "queue_depth",
				Help:      "Number of pending stack scans in the queue.",
			}, func() float64 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				val, err := q.QueueDepth(ctx)
				if err != nil {
					return 0
				}
				return float64(val)
			}),
		)

		state := &eventState{
			scanStatus:  make(map[string]string),
			stackStatus: make(map[string]string),
			stackStart:  make(map[string]time.Time),
		}
		go consumeEvents(q, state)
	})
}

func consumeEvents(q *queue.Queue, state *eventState) {
	pubsub := q.Client().PSubscribe(context.Background(), repoEventsPattern)
	for msg := range pubsub.Channel() {
		var event queue.RepoEvent
		if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
			continue
		}
		handleEvent(state, &event)
	}
}

func handleEvent(state *eventState, event *queue.RepoEvent) {
	switch event.Type {
	case "scan_update":
		updateScanMetrics(state, event)
	case "stack_update":
		updateStackMetrics(state, event)
	}
}

func updateScanMetrics(state *eventState, event *queue.RepoEvent) {
	if event.RepoName == "" || event.ScanID == "" || event.Status == "" {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	prev := state.scanStatus[event.ScanID]
	if prev == event.Status {
		return
	}
	state.scanStatus[event.ScanID] = event.Status

	switch event.Status {
	case "running":
		activeScans.WithLabelValues(event.RepoName).Inc()
	case "completed":
		if prev == "running" {
			activeScans.WithLabelValues(event.RepoName).Dec()
		}
		scansCompleted.WithLabelValues(event.RepoName).Inc()
		delete(state.scanStatus, event.ScanID)
	case "failed":
		if prev == "running" {
			activeScans.WithLabelValues(event.RepoName).Dec()
		}
		scansFailed.WithLabelValues(event.RepoName).Inc()
		delete(state.scanStatus, event.ScanID)
	case "canceled":
		if prev == "running" {
			activeScans.WithLabelValues(event.RepoName).Dec()
		}
		scansCanceled.WithLabelValues(event.RepoName).Inc()
		delete(state.scanStatus, event.ScanID)
	}
}

func updateStackMetrics(state *eventState, event *queue.RepoEvent) {
	if event.RepoName == "" || event.StackPath == "" || event.Status == "" {
		return
	}

	key := event.RepoName + "|" + event.ScanID + "|" + event.StackPath

	state.mu.Lock()
	defer state.mu.Unlock()

	prev := state.stackStatus[key]
	if prev == event.Status {
		return
	}
	state.stackStatus[key] = event.Status

	switch event.Status {
	case "running":
		if event.RunAt != nil {
			state.stackStart[key] = *event.RunAt
		} else {
			state.stackStart[key] = time.Now()
		}
	case "completed":
		stackCompleted.WithLabelValues(event.RepoName).Inc()
		if event.Drifted != nil && *event.Drifted {
			stackDrifted.WithLabelValues(event.RepoName).Inc()
		}
		observeStackDuration(state, key, event.RepoName)
		delete(state.stackStatus, key)
	case "failed":
		stackFailed.WithLabelValues(event.RepoName).Inc()
		observeStackDuration(state, key, event.RepoName)
		delete(state.stackStatus, key)
	case "canceled":
		observeStackDuration(state, key, event.RepoName)
		delete(state.stackStatus, key)
	}
}

func observeStackDuration(state *eventState, key, repo string) {
	start, ok := state.stackStart[key]
	if !ok {
		return
	}
	delete(state.stackStart, key)
	stackDuration.WithLabelValues(repo).Observe(time.Since(start).Seconds())
}
