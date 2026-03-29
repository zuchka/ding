package metrics_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/zuchka/ding/internal/metrics"
)

func TestCollector_IncrEvents(t *testing.T) {
	c := metrics.NewCollector()
	c.IncrEvents(5)
	c.IncrEvents(3)
	var buf strings.Builder
	c.WritePrometheus(&buf, 0)
	if !strings.Contains(buf.String(), "ding_events_ingested_total 8") {
		t.Errorf("expected ding_events_ingested_total 8, got:\n%s", buf.String())
	}
}

func TestCollector_IncrAlerts_PerRule(t *testing.T) {
	c := metrics.NewCollector()
	c.IncrAlerts("cpu_spike")
	c.IncrAlerts("cpu_spike")
	c.IncrAlerts("mem_high")
	var buf strings.Builder
	c.WritePrometheus(&buf, 0)
	out := buf.String()
	if !strings.Contains(out, `ding_alerts_fired_total{rule="cpu_spike"} 2`) {
		t.Errorf("expected cpu_spike=2, got:\n%s", out)
	}
	if !strings.Contains(out, `ding_alerts_fired_total{rule="mem_high"} 1`) {
		t.Errorf("expected mem_high=1, got:\n%s", out)
	}
}

func TestCollector_WebhookCounters(t *testing.T) {
	c := metrics.NewCollector()
	c.IncrWebhookSuccess()
	c.IncrWebhookSuccess()
	c.IncrWebhookDrop()
	c.IncrWebhookFailed()
	var buf strings.Builder
	c.WritePrometheus(&buf, 0)
	out := buf.String()
	if !strings.Contains(out, `ding_webhook_deliveries_total{result="success"} 2`) {
		t.Errorf("expected success=2, got:\n%s", out)
	}
	if !strings.Contains(out, `ding_webhook_deliveries_total{result="dropped"} 1`) {
		t.Errorf("expected dropped=1, got:\n%s", out)
	}
	if !strings.Contains(out, `ding_webhook_deliveries_total{result="failed"} 1`) {
		t.Errorf("expected failed=1, got:\n%s", out)
	}
}

func TestCollector_QueueDepth(t *testing.T) {
	c := metrics.NewCollector()
	var buf strings.Builder
	c.WritePrometheus(&buf, 42)
	if !strings.Contains(buf.String(), "ding_webhook_queue_depth 42") {
		t.Errorf("expected queue_depth 42, got:\n%s", buf.String())
	}
}

func TestCollector_UptimeSeconds(t *testing.T) {
	c := metrics.NewCollector()
	var buf strings.Builder
	c.WritePrometheus(&buf, 0)
	if !strings.Contains(buf.String(), "ding_uptime_seconds") {
		t.Errorf("expected ding_uptime_seconds in output, got:\n%s", buf.String())
	}
}

func TestCollector_WritePrometheus_HasHelpAndType(t *testing.T) {
	c := metrics.NewCollector()
	var buf strings.Builder
	c.WritePrometheus(&buf, 0)
	out := buf.String()
	for _, metric := range []string{
		"ding_events_ingested_total",
		"ding_alerts_fired_total",
		"ding_webhook_deliveries_total",
		"ding_webhook_queue_depth",
		"ding_uptime_seconds",
	} {
		if !strings.Contains(out, "# HELP "+metric) {
			t.Errorf("missing # HELP for %s", metric)
		}
		if !strings.Contains(out, "# TYPE "+metric) {
			t.Errorf("missing # TYPE for %s", metric)
		}
	}
}

func TestCollector_Concurrent(t *testing.T) {
	c := metrics.NewCollector()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.IncrEvents(1)
			c.IncrAlerts("rule_a")
			c.IncrWebhookSuccess()
		}()
	}
	wg.Wait()

	var buf strings.Builder
	c.WritePrometheus(&buf, 0)
	out := buf.String()
	if !strings.Contains(out, "ding_events_ingested_total 50") {
		t.Errorf("expected events=50, got:\n%s", out)
	}
	if !strings.Contains(out, `ding_alerts_fired_total{rule="rule_a"} 50`) {
		t.Errorf("expected rule_a=50, got:\n%s", out)
	}
	if !strings.Contains(out, `ding_webhook_deliveries_total{result="success"} 50`) {
		t.Errorf("expected success=50, got:\n%s", out)
	}
}
