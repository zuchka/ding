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
	"github.com/zuchka/ding/internal/ingester"
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
	return server.New(eng, notifiers, cfg, "", nil, nil, nil)
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
	return server.New(eng, notifiers, cfg, "", nil, nil, nil)
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
	return server.New(eng, notifiers, cfg, "", c, nil, nil), c
}

func makeServerWithJQ(t *testing.T, jqExpr string) *server.Server {
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
	cfg := &config.Config{Server: config.ServerConfig{Format: "json", MaxBodyBytes: 1 << 20, JQ: jqExpr}}
	jqCode, err := ingester.CompileJQ(jqExpr)
	if err != nil {
		t.Fatal(err)
	}
	return server.New(eng, notifiers, cfg, "", nil, nil, jqCode)
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
	srv := server.New(eng, notifiers, cfg, "", nil, al, nil)

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

func TestIngest_WithJQ_SingleObject(t *testing.T) {
	srv := makeServerWithJQ(t, `{metric: .name, value: .reading}`)
	body := `{"name":"cpu_usage","reading":97}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
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
	if resp["alerts_fired"] != 1 {
		t.Errorf("expected alerts_fired=1, got %d", resp["alerts_fired"])
	}
}

func TestIngest_WithJQ_ArrayFanout(t *testing.T) {
	srv := makeServerWithJQ(t, `.events[]`)
	// Two events: one above threshold, one below.
	body := `{"events":[{"metric":"cpu_usage","value":97},{"metric":"cpu_usage","value":50}]}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]int
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["events"] != 2 {
		t.Errorf("expected events=2, got %d", resp["events"])
	}
	if resp["alerts_fired"] != 1 {
		t.Errorf("expected alerts_fired=1, got %d", resp["alerts_fired"])
	}
}

func TestIngest_WithJQ_BadPayload_Returns400(t *testing.T) {
	// JQ maps .name→metric, .reading→value. Payload missing those fields produces
	// null metric/value, which ParseJSONLine rejects with a 400.
	srv := makeServerWithJQ(t, `{metric: .name, value: .reading}`)
	body := `{"foo":"bar","baz":1}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for payload with missing JQ fields, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReload_Inline_UpdatesJQ(t *testing.T) {
	// Config with JQ transform
	cfg1 := `
server:
  max_body_bytes: 1048576
  jq: '{metric: .name, value: .v}'
rules:
  - name: cpu_spike
    match:
      metric: cpu_usage
    condition: "value > 90"
    alert:
      - notifier: stdout
`
	// Config without JQ (standard format)
	cfg2 := `
server:
  max_body_bytes: 1048576
rules:
  - name: cpu_spike
    match:
      metric: cpu_usage
    condition: "value > 90"
    alert:
      - notifier: stdout
`
	f, err := os.CreateTemp(t.TempDir(), "ding-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(cfg1)
	f.Close()

	eng, cfg, notifiers, alertLogger, jqCode, err := server.BuildFromConfig(f.Name(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = alertLogger
	srv := server.New(eng, notifiers, cfg, f.Name(), nil, nil, jqCode)

	// Pre-reload: JQ format works
	req1 := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(`{"name":"cpu_usage","v":97}`))
	w1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("pre-reload: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}

	// Switch to config without JQ and reload
	os.WriteFile(f.Name(), []byte(cfg2), 0644)
	reloadReq := httptest.NewRequest(http.MethodPost, "/reload", nil)
	reloadW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(reloadW, reloadReq)
	if reloadW.Code != http.StatusOK {
		t.Fatalf("reload: expected 200, got %d: %s", reloadW.Code, reloadW.Body.String())
	}

	// Post-reload: standard format works
	req2 := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(`{"metric":"cpu_usage","value":97}`))
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("post-reload: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestSwapEngine_UpdatesJQ(t *testing.T) {
	// Start with JQ that maps .name/.v to metric/value
	srv := makeServerWithJQ(t, `{metric: .name, value: .v}`)

	// JQ format works before swap
	req1 := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(`{"name":"cpu_usage","v":97}`))
	w1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("pre-swap: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}

	// Swap to a server with no JQ (nil jqCode)
	rules := []evaluator.EngineRule{
		{
			Name:      "cpu_spike",
			Match:     map[string]string{"metric": "cpu_usage"},
			Condition: "value > 90",
			Alerts:    []string{"stdout"},
		},
	}
	newEng, err := evaluator.NewEngine(rules, 1000)
	if err != nil {
		t.Fatal(err)
	}
	newNotifiers := map[string]notifier.Notifier{
		"stdout": notifier.NewStdoutNotifier(bytes.NewBuffer(nil)),
	}
	newCfg := &config.Config{Server: config.ServerConfig{Format: "json", MaxBodyBytes: 1 << 20}}
	srv.SwapEngine(newEng, newCfg, newNotifiers, nil, nil)

	// After swap: standard format works, JQ format no longer applies
	req2 := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(`{"metric":"cpu_usage","value":97}`))
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("post-swap: expected 200 for standard format, got %d: %s", w2.Code, w2.Body.String())
	}
}
