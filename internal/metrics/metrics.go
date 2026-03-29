package metrics

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Collector holds all process-lifetime metrics for DING.
// Create once at startup with NewCollector; never recreate on hot-reload.
type Collector struct {
	startTime time.Time

	eventsTotal    atomic.Int64
	webhookSuccess atomic.Int64
	webhookDropped atomic.Int64
	webhookFailed  atomic.Int64

	alertsMu    sync.Mutex
	alertsTotal map[string]int64 // rule name → fired count
}

// NewCollector creates a Collector. Call once at process start.
func NewCollector() *Collector {
	return &Collector{
		startTime:   time.Now(),
		alertsTotal: make(map[string]int64),
	}
}

// IncrEvents adds n to the total events-ingested counter.
func (c *Collector) IncrEvents(n int64) { c.eventsTotal.Add(n) }

// IncrAlerts increments the per-rule alert counter.
func (c *Collector) IncrAlerts(rule string) {
	c.alertsMu.Lock()
	c.alertsTotal[rule]++
	c.alertsMu.Unlock()
}

// IncrWebhookSuccess increments the successful webhook delivery counter.
func (c *Collector) IncrWebhookSuccess() { c.webhookSuccess.Add(1) }

// IncrWebhookDrop increments the dropped webhook counter (queue full).
func (c *Collector) IncrWebhookDrop() { c.webhookDropped.Add(1) }

// IncrWebhookFailed increments the failed webhook counter (max attempts exhausted).
func (c *Collector) IncrWebhookFailed() { c.webhookFailed.Add(1) }

// WritePrometheus emits Prometheus text format (exposition format 0.0.4) to w.
// queueDepth is the sum of all webhook notifier queue depths, computed by the caller.
func (c *Collector) WritePrometheus(w io.Writer, queueDepth int) {
	uptime := time.Since(c.startTime).Seconds()

	fmt.Fprintf(w, "# HELP ding_events_ingested_total Total events processed since startup\n")
	fmt.Fprintf(w, "# TYPE ding_events_ingested_total counter\n")
	fmt.Fprintf(w, "ding_events_ingested_total %d\n", c.eventsTotal.Load())

	fmt.Fprintf(w, "# HELP ding_alerts_fired_total Alerts fired per rule since startup\n")
	fmt.Fprintf(w, "# TYPE ding_alerts_fired_total counter\n")
	c.alertsMu.Lock()
	for rule, count := range c.alertsTotal {
		fmt.Fprintf(w, "ding_alerts_fired_total{rule=%q} %d\n", rule, count)
	}
	c.alertsMu.Unlock()

	fmt.Fprintf(w, "# HELP ding_webhook_deliveries_total Webhook delivery outcomes since startup\n")
	fmt.Fprintf(w, "# TYPE ding_webhook_deliveries_total counter\n")
	fmt.Fprintf(w, "ding_webhook_deliveries_total{result=\"success\"} %d\n", c.webhookSuccess.Load())
	fmt.Fprintf(w, "ding_webhook_deliveries_total{result=\"dropped\"} %d\n", c.webhookDropped.Load())
	fmt.Fprintf(w, "ding_webhook_deliveries_total{result=\"failed\"} %d\n", c.webhookFailed.Load())

	fmt.Fprintf(w, "# HELP ding_webhook_queue_depth Current total items waiting in webhook retry queues\n")
	fmt.Fprintf(w, "# TYPE ding_webhook_queue_depth gauge\n")
	fmt.Fprintf(w, "ding_webhook_queue_depth %d\n", queueDepth)

	fmt.Fprintf(w, "# HELP ding_uptime_seconds Seconds since process start\n")
	fmt.Fprintf(w, "# TYPE ding_uptime_seconds gauge\n")
	fmt.Fprintf(w, "ding_uptime_seconds %.3f\n", uptime)
}
