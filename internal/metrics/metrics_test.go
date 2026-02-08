package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsHandleEvents(t *testing.T) {
	activeScans = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "active_scans"}, []string{"repo"})
	scansCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "scans_completed_total"}, []string{"repo"})
	scansFailed = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "scans_failed_total"}, []string{"repo"})
	scansCanceled = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "scans_canceled_total"}, []string{"repo"})
	stackCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "stack_scans_completed_total"}, []string{"repo"})
	stackFailed = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "stack_scans_failed_total"}, []string{"repo"})
	stackDrifted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "stack_scans_drifted_total"}, []string{"repo"})
	stackDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "stack_scan_duration_seconds"}, []string{"repo"})

	state := &eventState{
		scanStatus:  map[string]string{},
		stackStatus: map[string]string{},
		stackStart:  map[string]time.Time{},
	}

	updateScanMetrics(state, &queue.RepoEvent{Type: "scan_update", RepoName: "repo", ScanID: "scan1", Status: "running"})
	if got := testutil.ToFloat64(activeScans.WithLabelValues("repo")); got != 1 {
		t.Fatalf("active scans: got %v, want 1", got)
	}

	updateScanMetrics(state, &queue.RepoEvent{Type: "scan_update", RepoName: "repo", ScanID: "scan1", Status: "completed"})
	if got := testutil.ToFloat64(activeScans.WithLabelValues("repo")); got != 0 {
		t.Fatalf("active scans after complete: got %v, want 0", got)
	}
	if got := testutil.ToFloat64(scansCompleted.WithLabelValues("repo")); got != 1 {
		t.Fatalf("completed scans: got %v, want 1", got)
	}

	now := time.Now()
	updateStackMetrics(state, &queue.RepoEvent{Type: "stack_update", RepoName: "repo", ScanID: "scan1", StackPath: "stack", Status: "running", RunAt: &now})
	drifted := true
	updateStackMetrics(state, &queue.RepoEvent{Type: "stack_update", RepoName: "repo", ScanID: "scan1", StackPath: "stack", Status: "completed", Drifted: &drifted})

	if got := testutil.ToFloat64(stackCompleted.WithLabelValues("repo")); got != 1 {
		t.Fatalf("completed stacks: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(stackDrifted.WithLabelValues("repo")); got != 1 {
		t.Fatalf("drifted stacks: got %v, want 1", got)
	}

	if count := testutil.CollectAndCount(stackDuration); count == 0 {
		t.Fatalf("expected histogram to be collected")
	}
}

func TestMetricsEndpointExposesQueueDepth(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "driftd_queue_depth"})
	g.Set(3)
	if err := reg.Register(g); err != nil {
		t.Fatalf("register gauge: %v", err)
	}

	out, err := testutil.GatherAndCount(reg)
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if out != 1 {
		t.Fatalf("expected 1 metric family, got %d", out)
	}

	metricFamily, err := reg.Gather()
	if err != nil || len(metricFamily) != 1 {
		t.Fatalf("expected 1 metric family, got %v", err)
	}
	if !strings.Contains(metricFamily[0].GetName(), "driftd_queue_depth") {
		t.Fatalf("expected driftd_queue_depth in metrics")
	}
}
