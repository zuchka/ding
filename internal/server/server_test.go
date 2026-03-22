package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/super-ding/ding/internal/config"
	"github.com/super-ding/ding/internal/evaluator"
	"github.com/super-ding/ding/internal/notifier"
	"github.com/super-ding/ding/internal/server"
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
	return server.New(eng, notifiers, cfg, "")
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
	return server.New(eng, notifiers, cfg, "")
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
