package ingester_test

import (
	"strings"
	"testing"
	"time"

	"github.com/super-ding/ding/internal/ingester"
)

func TestParseJSONLine_Basic(t *testing.T) {
	events, err := ingester.ParseJSONLine([]byte(`{"metric":"cpu_usage","value":92.5,"host":"web-01"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Metric != "cpu_usage" {
		t.Errorf("expected metric cpu_usage, got %s", e.Metric)
	}
	if e.Value != 92.5 {
		t.Errorf("expected value 92.5, got %f", e.Value)
	}
	if e.Labels["host"] != "web-01" {
		t.Errorf("expected host web-01, got %s", e.Labels["host"])
	}
	if _, ok := e.Labels["timestamp"]; ok {
		t.Error("timestamp should not appear in labels")
	}
}

func TestParseJSONLine_TimestampOverride(t *testing.T) {
	events, err := ingester.ParseJSONLine([]byte(`{"metric":"cpu_usage","value":92.5,"timestamp":1711111111.5}`))
	if err != nil {
		t.Fatal(err)
	}
	e := events[0]
	expected := time.Unix(1711111111, 500000000)
	if !e.At.Equal(expected) {
		t.Errorf("expected timestamp %v, got %v", expected, e.At)
	}
}

func TestParseJSONLine_MissingMetric(t *testing.T) {
	_, err := ingester.ParseJSONLine([]byte(`{"value":92.5}`))
	if err == nil {
		t.Fatal("expected error for missing metric")
	}
}

func TestParseJSONLine_InvalidJSON(t *testing.T) {
	_, err := ingester.ParseJSONLine([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParsePrometheusText_Basic(t *testing.T) {
	input := strings.Join([]string{
		`# HELP cpu_usage Current CPU usage`,
		`# TYPE cpu_usage gauge`,
		`cpu_usage{host="web-01"} 92.5`,
		`http_errors{host="api-02",region="us-east"} 5`,
	}, "\n")

	events, err := ingester.ParsePrometheusText([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Metric != "cpu_usage" || events[0].Value != 92.5 {
		t.Errorf("unexpected first event: %+v", events[0])
	}
	if events[0].Labels["host"] != "web-01" {
		t.Errorf("expected host web-01, got %s", events[0].Labels["host"])
	}
	if events[1].Labels["region"] != "us-east" {
		t.Errorf("expected region us-east, got %s", events[1].Labels["region"])
	}
}

func TestParsePrometheusText_NoLabels(t *testing.T) {
	events, err := ingester.ParsePrometheusText([]byte("cpu_usage 42.0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Value != 42.0 {
		t.Errorf("unexpected events: %+v", events)
	}
}

func TestDetectFormat_JSONByContentType(t *testing.T) {
	format := ingester.DetectFormat(nil, "application/json", "auto")
	if format != "json" {
		t.Errorf("expected json, got %s", format)
	}
}

func TestDetectFormat_PrometheusHeuristic(t *testing.T) {
	data := []byte(`cpu_usage{host="web-01"} 92.5`)
	format := ingester.DetectFormat(data, "", "auto")
	if format != "prometheus" {
		t.Errorf("expected prometheus, got %s", format)
	}
}

func TestDetectFormat_JSONHeuristic(t *testing.T) {
	data := []byte(`{"metric":"cpu","value":1}`)
	format := ingester.DetectFormat(data, "", "auto")
	if format != "json" {
		t.Errorf("expected json, got %s", format)
	}
}

func TestDetectFormat_ServerFormatOverrides(t *testing.T) {
	format := ingester.DetectFormat(nil, "application/json", "prometheus")
	if format != "prometheus" {
		t.Errorf("expected prometheus (server override), got %s", format)
	}
}
