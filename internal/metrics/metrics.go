package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/queue"
	"github.com/prometheus/client_golang/prometheus"
)

var registerOnce sync.Once

func Register(q *queue.Queue) {
	registerOnce.Do(func() {
		if q == nil {
			return
		}
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
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
		}))
	})
}
