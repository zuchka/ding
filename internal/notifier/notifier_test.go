package notifier_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/super-ding/ding/internal/evaluator"
	"github.com/super-ding/ding/internal/notifier"
)

func makeAlert() evaluator.Alert {
	return evaluator.Alert{
		Rule:    "cpu_spike",
		Message: "CPU spike on web-01: 97%",
		Metric:  "cpu_usage",
		Value:   97,
		Labels:  map[string]string{"host": "web-01"},
		FiredAt: time.Date(2026, 3, 22, 14, 30, 0, 0, time.UTC),
	}
}

func TestStdoutNotifier_WritesJSON(t *testing.T) {
	var buf bytes.Buffer
	n := notifier.NewStdoutNotifier(&buf)
	if err := n.Send(makeAlert()); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, line)
	}
	if out["rule"] != "cpu_spike" {
		t.Errorf("expected rule cpu_spike, got %v", out["rule"])
	}
	if out["metric"] != "cpu_usage" {
		t.Errorf("expected metric cpu_usage, got %v", out["metric"])
	}
	if out["host"] != "web-01" {
		t.Errorf("expected host web-01 in output, got %v", out["host"])
	}
}

func TestWebhookNotifier_PostsJSON(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := notifier.NewWebhookNotifier(srv.URL)
	if err := n.Send(makeAlert()); err != nil {
		t.Fatal(err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(received, &out); err != nil {
		t.Fatalf("webhook received invalid JSON: %v", err)
	}
	if out["rule"] != "cpu_spike" {
		t.Errorf("expected rule cpu_spike in webhook payload, got %v", out["rule"])
	}
}

func TestWebhookNotifier_FailsSilently(t *testing.T) {
	n := notifier.NewWebhookNotifier("http://127.0.0.1:1") // nothing listening
	// Should not return an error — failure is logged and dropped
	if err := n.Send(makeAlert()); err != nil {
		t.Errorf("expected silent failure, got error: %v", err)
	}
}

func TestWebhookNotifier_FailsSilentlyOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	n := notifier.NewWebhookNotifier(srv.URL)
	if err := n.Send(makeAlert()); err != nil {
		t.Errorf("expected silent failure on 4xx, got error: %v", err)
	}
}
