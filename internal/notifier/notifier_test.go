package notifier_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	delivered := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		delivered <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := notifier.NewWebhookNotifier(srv.URL, 3, 1*time.Millisecond)
	defer n.Stop()
	if err := n.Send(makeAlert()); err != nil {
		t.Fatal(err)
	}

	select {
	case received := <-delivered:
		var out map[string]interface{}
		if err := json.Unmarshal(received, &out); err != nil {
			t.Fatalf("webhook received invalid JSON: %v", err)
		}
		if out["rule"] != "cpu_spike" {
			t.Errorf("expected rule cpu_spike in webhook payload, got %v", out["rule"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for webhook delivery")
	}
}

func TestWebhookNotifier_FailsSilently(t *testing.T) {
	n := notifier.NewWebhookNotifier("http://127.0.0.1:1", 3, 1*time.Millisecond) // nothing listening
	defer n.Stop()
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

	n := notifier.NewWebhookNotifier(srv.URL, 3, 1*time.Millisecond)
	defer n.Stop()
	if err := n.Send(makeAlert()); err != nil {
		t.Errorf("expected silent failure on 4xx, got error: %v", err)
	}
}

// TestWebhookNotifier_RetriesOnServerError: server returns 500 twice then 200.
// Assert the alert is eventually delivered.
func TestWebhookNotifier_RetriesOnServerError(t *testing.T) {
	var count atomic.Int32
	delivered := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
			delivered <- struct{}{}
		}
	}))
	defer srv.Close()

	n := notifier.NewWebhookNotifier(srv.URL, 5, 1*time.Millisecond)
	defer n.Stop()

	if err := n.Send(makeAlert()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-delivered:
		if got := count.Load(); got != 3 {
			t.Errorf("expected 3 requests (2 failures + 1 success), got %d", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for delivery; got %d requests", count.Load())
	}
}

// TestWebhookNotifier_NoRetryOn4xx: server always returns 400; exactly 1 POST should be made.
func TestWebhookNotifier_NoRetryOn4xx(t *testing.T) {
	var count atomic.Int32
	done := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		// Signal after first request
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	n := notifier.NewWebhookNotifier(srv.URL, 3, 1*time.Millisecond)
	defer n.Stop()

	if err := n.Send(makeAlert()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first request")
	}

	// Give a brief moment to ensure no retry happens.
	time.Sleep(50 * time.Millisecond)

	if got := count.Load(); got != 1 {
		t.Errorf("expected exactly 1 POST for 4xx, got %d", got)
	}
}

// TestWebhookNotifier_DropsAfterMaxAttempts: server always returns 500, maxAttempts=2;
// assert exactly 2 POSTs are made.
func TestWebhookNotifier_DropsAfterMaxAttempts(t *testing.T) {
	var count atomic.Int32
	secondRequest := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		if n == 2 {
			select {
			case secondRequest <- struct{}{}:
			default:
			}
		}
	}))
	defer srv.Close()

	n := notifier.NewWebhookNotifier(srv.URL, 2, 1*time.Millisecond)
	defer n.Stop()

	if err := n.Send(makeAlert()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-secondRequest:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for second request; got %d requests", count.Load())
	}

	// Give a brief moment to confirm no further retries happen.
	time.Sleep(50 * time.Millisecond)

	if got := count.Load(); got != 2 {
		t.Errorf("expected exactly 2 POSTs (maxAttempts=2), got %d", got)
	}
}

// TestWebhookNotifier_QueueFull_DropsSilently: Stop immediately, then Send many times.
// Assert Send always returns nil and doesn't block.
func TestWebhookNotifier_QueueFull_DropsSilently(t *testing.T) {
	// Create a notifier and immediately stop the worker so the queue won't drain.
	n := notifier.NewWebhookNotifier("http://127.0.0.1:1", 3, 1*time.Millisecond)
	n.Stop()

	// Give the worker goroutine time to actually exit.
	time.Sleep(10 * time.Millisecond)

	// Send 300 alerts — queue capacity is 256, so many will be dropped.
	// All calls must return nil and must not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 300; i++ {
			if err := n.Send(makeAlert()); err != nil {
				t.Errorf("Send returned non-nil error: %v", err)
			}
		}
		close(done)
	}()

	select {
	case <-done:
		// Success: all sends returned without blocking.
	case <-time.After(3 * time.Second):
		t.Fatal("Send blocked — queue should drop when full")
	}
}

// TestWebhookNotifier_WorkerExitsOnStop: start a notifier, send an alert, call Stop(),
// verify the goroutine exits without hanging.
func TestWebhookNotifier_WorkerExitsOnStop(t *testing.T) {
	// Use a server that blocks indefinitely to keep the worker busy.
	waiting := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case waiting <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	n := notifier.NewWebhookNotifier(srv.URL, 3, 1*time.Millisecond)

	if err := n.Send(makeAlert()); err != nil {
		t.Fatal(err)
	}

	// Wait for the worker to be inside the HTTP request.
	select {
	case <-waiting:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for worker to start request")
	}

	// Stop should return promptly (worker is blocked in deliver, but stop channel
	// closes; after the HTTP call returns the worker will see stop).
	stopped := make(chan struct{})
	go func() {
		n.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		// Good — Stop() returned without hanging.
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() blocked unexpectedly")
	}
}
