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
	activeScans = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "active_scans"}, []string{"project"})
	scansCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "scans_completed_total"}, []string{"project"})
	scansFailed = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "scans_failed_total"}, []string{"project"})
	scansCanceled = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "scans_canceled_total"}, []string{"project"})
	stackCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "stack_scans_completed_total"}, []string{"project"})
	stackFailed = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "stack_scans_failed_total"}, []string{"project"})
	stackDrifted = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "stack_scans_drifted_total"}, []string{"project"})
	stackDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "stack_scan_duration_seconds"}, []string{"project"})

	state := &eventState{
		scanStatus:  map[string]string{},
		stackStatus: map[string]string{},
		stackStart:  map[string]time.Time{},
	}

	updateScanMetrics(state, &queue.ProjectEvent{Type: "scan_update", ProjectName: "project", ScanID: "scan1", Status: "running"})
	if got := testutil.ToFloat64(activeScans.WithLabelValues("project")); got != 1 {
		t.Fatalf("active scans: got %v, want 1", got)
	}

	updateScanMetrics(state, &queue.ProjectEvent{Type: "scan_update", ProjectName: "project", ScanID: "scan1", Status: "completed"})
	if got := testutil.ToFloat64(activeScans.WithLabelValues("project")); got != 0 {
		t.Fatalf("active scans after complete: got %v, want 0", got)
	}
	if got := testutil.ToFloat64(scansCompleted.WithLabelValues("project")); got != 1 {
		t.Fatalf("completed scans: got %v, want 1", got)
	}

	now := time.Now()
	updateStackMetrics(state, &queue.ProjectEvent{Type: "stack_update", ProjectName: "project", ScanID: "scan1", StackPath: "stack", Status: "running", RunAt: &now})
	drifted := true
	updateStackMetrics(state, &queue.ProjectEvent{Type: "stack_update", ProjectName: "project", ScanID: "scan1", StackPath: "stack", Status: "completed", Drifted: &drifted})

	if got := testutil.ToFloat64(stackCompleted.WithLabelValues("project")); got != 1 {
		t.Fatalf("completed stacks: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(stackDrifted.WithLabelValues("project")); got != 1 {
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
