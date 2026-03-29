package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zuchka/ding/internal/config"
	"github.com/zuchka/ding/internal/evaluator"
	"github.com/zuchka/ding/internal/metrics"
	"github.com/zuchka/ding/internal/notifier"
	"github.com/zuchka/ding/internal/server"
)

func makeServer(t *testing.T) *server.Server {
	t.Helper()
	rules := []evaluator.EngineRule{
		{
			Name:      "cpu_spike",
			Match:     map[string]string{"metric": "cpu_usage"},
			Condition: "value > 90",
			Alerts:    []string{"stdout"},
		},
	}
	eng, err := evaluator.NewEngine(rules, 1000)
	if err != nil {
		t.Fatal(err)
	}
	notifiers := map[string]notifier.Notifier{
		"stdout": notifier.NewStdoutNotifier(bytes.NewBuffer(nil)),
	}
	cfg := &config.Config{Server: config.ServerConfig{Format: "json", MaxBodyBytes: 1 << 20}}
	return server.New(eng, notifiers, cfg, "", nil, nil)
}

func TestHealth(t *testing.T) {
	srv := makeServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestIngest_ValidJSON_NoAlert(t *testing.T) {
	srv := makeServer(t)
	body := `{"metric":"cpu_usage","value":50,"host":"web-01"}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]int
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["events"] != 1 {
		t.Errorf("expected events=1, got %d", resp["events"])
	}
	if resp["alerts_fired"] != 0 {
		t.Errorf("expected alerts_fired=0, got %d", resp["alerts_fired"])
	}
}

func TestIngest_ValidJSON_Alert(t *testing.T) {
	srv := makeServer(t)
	body := `{"metric":"cpu_usage","value":97,"host":"web-01"}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]int
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["alerts_fired"] != 1 {
		t.Errorf("expected alerts_fired=1, got %d", resp["alerts_fired"])
	}
}

func TestIngest_InvalidJSON(t *testing.T) {
	srv := makeServer(t)
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestRulesEndpoint(t *testing.T) {
	srv := makeServer(t)
	req := httptest.NewRequest(http.MethodGet, "/rules", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var rules []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &rules); err != nil {
		t.Fatalf("response is not JSON array: %v", err)
	}
	if len(rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(rules))
	}
}

func TestIngest_MethodNotAllowed(t *testing.T) {
	srv := makeServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestReload_NoConfigPath(t *testing.T) {
	srv := makeServer(t) // configPath is ""
	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func makeServerWithMaxBodyBytes(t *testing.T, maxBodyBytes int64) *server.Server {
	t.Helper()
	rules := []evaluator.EngineRule{
		{
			Name:      "cpu_spike",
			Match:     map[string]string{"metric": "cpu_usage"},
			Condition: "value > 90",
			Alerts:    []string{"stdout"},
		},
	}
	eng, err := evaluator.NewEngine(rules, 1000)
	if err != nil {
		t.Fatal(err)
	}
	notifiers := map[string]notifier.Notifier{
		"stdout": notifier.NewStdoutNotifier(bytes.NewBuffer(nil)),
	}
	cfg := &config.Config{Server: config.ServerConfig{Format: "json", MaxBodyBytes: maxBodyBytes}}
	return server.New(eng, notifiers, cfg, "", nil, nil)
}

func TestIngest_BodyTooLarge(t *testing.T) {
	// Set limit below the minimum valid event body size so any content triggers 413.
	srv := makeServerWithMaxBodyBytes(t, 10)
	body := bytes.Repeat([]byte("x"), 11)
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIngest_BodyAtLimit(t *testing.T) {
	// {"metric":"x","value":1} is 24 bytes — the smallest valid event body.
	// Set the limit to exactly 24 bytes; the request should succeed (200).
	body := []byte(`{"metric":"x","value":1}`)
	srv := makeServerWithMaxBodyBytes(t, int64(len(body)))
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIngest_BodyJustOverLimit(t *testing.T) {
	// Set limit to 1 byte below the body length so MaxBytesReader triggers 413.
	body := []byte(`{"metric":"x","value":1}`)
	srv := makeServerWithMaxBodyBytes(t, int64(len(body))-1)
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d: %s", w.Code, w.Body.String())
	}
}

// makeServerWithCollector creates a server wired with a real Collector for metrics tests.
func makeServerWithCollector(t *testing.T) (*server.Server, *metrics.Collector) {
	t.Helper()
	rules := []evaluator.EngineRule{
		{
			Name:      "cpu_spike",
			Match:     map[string]string{"metric": "cpu_usage"},
			Condition: "value > 90",
			Alerts:    []string{"stdout"},
		},
	}
	eng, err := evaluator.NewEngine(rules, 1000)
	if err != nil {
		t.Fatal(err)
	}
	notifiers := map[string]notifier.Notifier{
		"stdout": notifier.NewStdoutNotifier(bytes.NewBuffer(nil)),
	}
	cfg := &config.Config{Server: config.ServerConfig{Format: "json", MaxBodyBytes: 1 << 20}}
	c := metrics.NewCollector()
	return server.New(eng, notifiers, cfg, "", c, nil), c
}

func TestMetricsEndpoint_OK(t *testing.T) {
	srv, _ := makeServerWithCollector(t)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "ding_events_ingested_total") {
		t.Errorf("expected ding_events_ingested_total in response, got: %s", w.Body.String())
	}
}

func TestMetricsEndpoint_MethodNotAllowed(t *testing.T) {
	srv, _ := makeServerWithCollector(t)
	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestMetricsEndpoint_CountsEventsAndAlerts(t *testing.T) {
	srv, _ := makeServerWithCollector(t)

	// Ingest one alert-firing event.
	body := `{"metric":"cpu_usage","value":97,"host":"web-01"}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ingest failed: %d %s", w.Code, w.Body.String())
	}

	// Check /metrics output.
	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)

	body2 := w2.Body.String()
	if !strings.Contains(body2, "ding_events_ingested_total 1") {
		t.Errorf("expected ding_events_ingested_total 1 in metrics, got:\n%s", body2)
	}
	if !strings.Contains(body2, `ding_alerts_fired_total{rule="cpu_spike"} 1`) {
		t.Errorf("expected ding_alerts_fired_total{rule=\"cpu_spike\"} 1, got:\n%s", body2)
	}
}

func TestMetricsEndpoint_NoCollector(t *testing.T) {
	// A server created with nil collector should return 503.
	srv := makeServer(t) // nil collector
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 with nil collector, got %d", w.Code)
	}
}

func TestAlertLog_WritesOnAlert(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "alerts-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	al, err := notifier.NewAlertLogger(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()

	rules := []evaluator.EngineRule{
		{
			Name:      "cpu_spike",
			Match:     map[string]string{"metric": "cpu_usage"},
			Condition: "value > 90",
			Alerts:    []string{"stdout"},
		},
	}
	eng, err := evaluator.NewEngine(rules, 1000)
	if err != nil {
		t.Fatal(err)
	}
	notifiers := map[string]notifier.Notifier{
		"stdout": notifier.NewStdoutNotifier(bytes.NewBuffer(nil)),
	}
	cfg := &config.Config{Server: config.ServerConfig{Format: "json", MaxBodyBytes: 1 << 20}}
	srv := server.New(eng, notifiers, cfg, "", nil, al)

	body := `{"metric":"cpu_usage","value":97,"host":"web-01"}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ingest failed: %d", w.Code)
	}

	al.Close()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		t.Fatal("expected alert log entry, got empty file")
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("alert log entry is not valid JSON: %v\nentry: %s", err, line)
	}
	if out["rule"] != "cpu_spike" {
		t.Errorf("expected rule=cpu_spike in alert log, got %v", out["rule"])
	}
}
