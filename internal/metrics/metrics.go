package metrics

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
	"github.com/prometheus/client_golang/prometheus"
)

const projectEventsPattern = "driftd:events:*"

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
		}, []string{"project"})

		scansCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "scans_completed_total",
			Help:      "Number of scans completed successfully.",
		}, []string{"project"})
		scansFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "scans_failed_total",
			Help:      "Number of scans that failed.",
		}, []string{"project"})
		scansCanceled = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "scans_canceled_total",
			Help:      "Number of scans that were canceled.",
		}, []string{"project"})

		stackCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "stack_scans_completed_total",
			Help:      "Number of stack scans completed successfully.",
		}, []string{"project"})
		stackFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "stack_scans_failed_total",
			Help:      "Number of stack scans that failed.",
		}, []string{"project"})
		stackDrifted = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "driftd",
			Name:      "stack_scans_drifted_total",
			Help:      "Number of stack scans that detected drift.",
		}, []string{"project"})

		stackDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "driftd",
			Name:      "stack_scan_duration_seconds",
			Help:      "Duration of stack scans in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"project"})

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
				Name:      "running_stack_scans",
				Help:      "Number of stack scans currently marked running.",
			}, func() float64 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				val, err := q.RunningStackScanCount(ctx)
				if err != nil {
					return 0
				}
				return float64(val)
			}),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Namespace: "driftd",
				Name:      "oldest_running_stack_scan_age_seconds",
				Help:      "Age of the oldest running stack scan in seconds.",
			}, func() float64 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				age, err := q.OldestRunningStackScanAge(ctx)
				if err != nil {
					return 0
				}
				return age.Seconds()
			}),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Namespace: "driftd",
				Name:      "running_scans",
				Help:      "Number of scans currently marked running.",
			}, func() float64 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				val, err := q.RunningScanCount(ctx)
				if err != nil {
					return 0
				}
				return float64(val)
			}),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Namespace: "driftd",
				Name:      "oldest_running_scan_age_seconds",
				Help:      "Age of the oldest running scan in seconds.",
			}, func() float64 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				age, err := q.OldestRunningScanAge(ctx)
				if err != nil {
					return 0
				}
				return age.Seconds()
			}),
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
	pubsub := q.Client().PSubscribe(context.Background(), projectEventsPattern)
	for msg := range pubsub.Channel() {
		var event queue.ProjectEvent
		if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
			continue
		}
		handleEvent(state, &event)
	}
}

func handleEvent(state *eventState, event *queue.ProjectEvent) {
	switch event.Type {
	case "scan_update":
		updateScanMetrics(state, event)
	case "stack_update":
		updateStackMetrics(state, event)
	}
}

func updateScanMetrics(state *eventState, event *queue.ProjectEvent) {
	if event.ProjectName == "" || event.ScanID == "" || event.Status == "" {
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
		activeScans.WithLabelValues(event.ProjectName).Inc()
	case "completed":
		if prev == "running" {
			activeScans.WithLabelValues(event.ProjectName).Dec()
		}
		scansCompleted.WithLabelValues(event.ProjectName).Inc()
		delete(state.scanStatus, event.ScanID)
	case "failed":
		if prev == "running" {
			activeScans.WithLabelValues(event.ProjectName).Dec()
		}
		scansFailed.WithLabelValues(event.ProjectName).Inc()
		delete(state.scanStatus, event.ScanID)
	case "canceled":
		if prev == "running" {
			activeScans.WithLabelValues(event.ProjectName).Dec()
		}
		scansCanceled.WithLabelValues(event.ProjectName).Inc()
		delete(state.scanStatus, event.ScanID)
	}
}

func updateStackMetrics(state *eventState, event *queue.ProjectEvent) {
	if event.ProjectName == "" || event.StackPath == "" || event.Status == "" {
		return
	}

	key := event.ProjectName + "|" + event.ScanID + "|" + event.StackPath

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
		stackCompleted.WithLabelValues(event.ProjectName).Inc()
		if event.Drifted != nil && *event.Drifted {
			stackDrifted.WithLabelValues(event.ProjectName).Inc()
		}
		observeStackDuration(state, key, event.ProjectName)
		delete(state.stackStatus, key)
	case "failed":
		stackFailed.WithLabelValues(event.ProjectName).Inc()
		observeStackDuration(state, key, event.ProjectName)
		delete(state.stackStatus, key)
	case "canceled":
		observeStackDuration(state, key, event.ProjectName)
		delete(state.stackStatus, key)
	}
}

func observeStackDuration(state *eventState, key, project string) {
	start, ok := state.stackStart[key]
	if !ok {
		return
	}
	delete(state.stackStart, key)
	stackDuration.WithLabelValues(project).Observe(time.Since(start).Seconds())
}
