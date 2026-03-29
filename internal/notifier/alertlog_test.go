package notifier_test

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zuchka/ding/internal/evaluator"
	"github.com/zuchka/ding/internal/notifier"
)

func makeAlertForLog() evaluator.Alert {
	return evaluator.Alert{
		Rule:    "cpu_spike",
		Message: "CPU spike",
		Metric:  "cpu_usage",
		Value:   97,
		Labels:  map[string]string{"host": "web-01"},
		FiredAt: time.Date(2026, 3, 22, 14, 30, 0, 0, time.UTC),
	}
}

func TestAlertLogger_WritesJSONLine(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "alerts-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	al, err := notifier.NewAlertLogger(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := al.Log(makeAlertForLog()); err != nil {
		t.Fatal(err)
	}
	al.Close()

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("not valid JSON: %v\nline: %s", err, line)
	}
	if out["rule"] != "cpu_spike" {
		t.Errorf("expected rule=cpu_spike, got %v", out["rule"])
	}
	if out["metric"] != "cpu_usage" {
		t.Errorf("expected metric=cpu_usage, got %v", out["metric"])
	}
	if _, ok := out["fired_at"]; !ok {
		t.Error("expected fired_at field in log entry")
	}
}

func TestAlertLogger_AppendMode(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "alerts-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	al, err := notifier.NewAlertLogger(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	al.Log(makeAlertForLog())
	al.Log(makeAlertForLog())
	al.Close()

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 log lines, got %d:\n%s", len(lines), string(data))
	}
	for i, line := range lines {
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestAlertLogger_Concurrent(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "alerts-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	al, err := notifier.NewAlertLogger(f.Name())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := al.Log(makeAlertForLog()); err != nil {
					t.Errorf("Log error: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	al.Close()

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 200 {
		t.Errorf("expected 200 log lines, got %d", len(lines))
	}
	for i, line := range lines {
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestAlertLogger_FileNotExist(t *testing.T) {
	_, err := notifier.NewAlertLogger("/nonexistent/path/alerts.jsonl")
	if err == nil {
		t.Error("expected error for non-existent directory, got nil")
	}
}
