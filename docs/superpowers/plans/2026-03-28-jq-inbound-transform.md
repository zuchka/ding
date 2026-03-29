# JQ Inbound Transform Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional `jq` field to the server config that transforms arbitrary inbound JSON payloads into ding's `{metric, value, labels}` event format before evaluation.

**Architecture:** JQ is compiled once at startup inside `buildFromConfig` and stored on the `Server` struct. `handleIngest` and `IngestLine` check for a non-nil `*gojq.Code` and call `ingester.RunJQ` instead of the normal parse path. All signature changes thread through `server.New`, `SwapEngine`, `BuildFromConfig`, and `cmd/ding/main.go`. The engine, rules, conditions, cooldowns, and notifiers are untouched.

**Tech Stack:** Go 1.22, `github.com/itchyny/gojq` (pure-Go JQ, no CGO), existing `gopkg.in/yaml.v3` for config.

**Spec:** `docs/superpowers/specs/2026-03-28-jq-inbound-transform-design.md`

---

## File Map

| Action | File | What changes |
|--------|------|-------------|
| Modify | `internal/config/config.go` | Add `JQ string \`yaml:"jq"\`` to `ServerConfig` |
| Create | `internal/ingester/jq.go` | `CompileJQ`, `RunJQ` |
| Create | `internal/ingester/jq_test.go` | Unit tests for `CompileJQ` and `RunJQ` |
| Modify | `internal/server/server.go` | `jqCode` field on `Server`; update `New`, `SwapEngine`, `buildFromConfig`, `BuildFromConfig`, `IngestLine` |
| Modify | `internal/server/handlers.go` | Update `handleIngest` and `handleReload` inline path |
| Modify | `internal/server/server_test.go` | Update all `server.New` calls; add `makeServerWithJQ`; add integration tests |
| Modify | `cmd/ding/main.go` | Update all 4 `BuildFromConfig` call sites; pass `jqCode` to `server.New` and `SwapEngine` |
| Modify | `go.mod`, `go.sum` | Add `github.com/itchyny/gojq` |

---

## Task 1: Add gojq dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/itchyny/gojq
```

- [ ] **Step 2: Verify go.mod updated**

```bash
grep gojq go.mod
```

Expected: a line like `github.com/itchyny/gojq v0.12.x`

- [ ] **Step 3: Run existing tests to confirm nothing is broken**

```bash
go test ./...
```

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add gojq dependency"
```

---

## Task 2: Add JQ field to ServerConfig

**Files:**
- Modify: `internal/config/config.go:23-31`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestLoad_JQField(t *testing.T) {
	yaml := `
server:
  port: 8080
  jq: '.events[] | {metric: .name, value: .v}'
rules:
  - name: test_rule
    condition: "value > 0"
    alert:
      - notifier: stdout
`
	f, err := os.CreateTemp("", "ding-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(yaml)
	f.Close()

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := ".events[] | {metric: .name, value: .v}"
	if cfg.Server.JQ != want {
		t.Errorf("expected JQ %q, got %q", want, cfg.Server.JQ)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/config/... -run TestLoad_JQField -v
```

Expected: FAIL — `cfg.Server.JQ` does not exist yet.

- [ ] **Step 3: Add `JQ` field to `ServerConfig`**

In `internal/config/config.go`, update `ServerConfig`:

```go
type ServerConfig struct {
	Port          int      `yaml:"port"`
	Format        string   `yaml:"format"`
	MaxBufferSize int      `yaml:"max_buffer_size"`
	ReadTimeout   Duration `yaml:"read_timeout"`
	WriteTimeout  Duration `yaml:"write_timeout"`
	IdleTimeout   Duration `yaml:"idle_timeout"`
	MaxBodyBytes  int64    `yaml:"max_body_bytes"`
	JQ            string   `yaml:"jq"`
}
```

No changes to `Validate()` are required — `JQ` is an optional string with no validation logic.

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/config/... -v
```

Expected: all pass including `TestLoad_JQField`.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add JQ field to ServerConfig"
```

---

## Task 3: Implement ingester/jq.go (TDD)

**Files:**
- Create: `internal/ingester/jq.go`
- Create: `internal/ingester/jq_test.go`

- [ ] **Step 1: Write all failing tests**

Create `internal/ingester/jq_test.go`:

```go
package ingester_test

import (
	"strings"
	"testing"

	"github.com/zuchka/ding/internal/ingester"
)

func TestCompileJQ_Valid(t *testing.T) {
	_, err := ingester.CompileJQ(`.x`)
	if err != nil {
		t.Fatalf("expected no error for valid expression, got %v", err)
	}
}

func TestCompileJQ_Invalid(t *testing.T) {
	_, err := ingester.CompileJQ(`not valid jq |||`)
	if err == nil {
		t.Fatal("expected error for invalid JQ expression, got nil")
	}
}

func TestRunJQ_SingleObject(t *testing.T) {
	code, _ := ingester.CompileJQ(`{metric: .name, value: .reading}`)
	events, err := ingester.RunJQ(code, []byte(`{"name":"cpu_usage","reading":95.0}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Metric != "cpu_usage" {
		t.Errorf("expected metric cpu_usage, got %s", events[0].Metric)
	}
	if events[0].Value != 95.0 {
		t.Errorf("expected value 95.0, got %f", events[0].Value)
	}
}

func TestRunJQ_ArrayOutput(t *testing.T) {
	code, _ := ingester.CompileJQ(`.events[]`)
	input := `{"events":[{"metric":"cpu_usage","value":80},{"metric":"mem_usage","value":60}]}`
	events, err := ingester.RunJQ(code, []byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Metric != "cpu_usage" {
		t.Errorf("expected first metric cpu_usage, got %s", events[0].Metric)
	}
	if events[1].Metric != "mem_usage" {
		t.Errorf("expected second metric mem_usage, got %s", events[1].Metric)
	}
}

func TestRunJQ_ExtraFieldsBecomeLabels(t *testing.T) {
	code, _ := ingester.CompileJQ(`{metric: .name, value: .v, host: .host}`)
	events, err := ingester.RunJQ(code, []byte(`{"name":"cpu_usage","v":50,"host":"web-01"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if events[0].Labels["host"] != "web-01" {
		t.Errorf("expected label host=web-01, got %q", events[0].Labels["host"])
	}
}

func TestRunJQ_NullOutput(t *testing.T) {
	code, _ := ingester.CompileJQ(`null`)
	_, err := ingester.RunJQ(code, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for null output, got nil")
	}
}

func TestRunJQ_EmptyArrayOutput(t *testing.T) {
	code, _ := ingester.CompileJQ(`.events[]`)
	_, err := ingester.RunJQ(code, []byte(`{"events":[]}`))
	if err == nil {
		t.Fatal("expected error for empty array output, got nil")
	}
	if !strings.Contains(err.Error(), "no output") {
		t.Errorf("expected 'no output' in error, got: %v", err)
	}
}

func TestRunJQ_NonObjectOutput_String(t *testing.T) {
	code, _ := ingester.CompileJQ(`.name`)
	_, err := ingester.RunJQ(code, []byte(`{"name":"cpu_usage"}`))
	if err == nil {
		t.Fatal("expected error for non-object output, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected output type") {
		t.Errorf("expected 'unexpected output type' in error, got: %v", err)
	}
}

func TestRunJQ_NonObjectOutput_Number(t *testing.T) {
	code, _ := ingester.CompileJQ(`.v`)
	_, err := ingester.RunJQ(code, []byte(`{"v":42}`))
	if err == nil {
		t.Fatal("expected error for number output, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected output type") {
		t.Errorf("expected 'unexpected output type' in error, got: %v", err)
	}
}

func TestRunJQ_MissingMetric(t *testing.T) {
	code, _ := ingester.CompileJQ(`{value: .v}`)
	_, err := ingester.RunJQ(code, []byte(`{"v":42}`))
	if err == nil {
		t.Fatal("expected error for missing metric field, got nil")
	}
}

func TestRunJQ_MissingValue(t *testing.T) {
	code, _ := ingester.CompileJQ(`{metric: .m}`)
	_, err := ingester.RunJQ(code, []byte(`{"m":"cpu_usage"}`))
	if err == nil {
		t.Fatal("expected error for missing value field, got nil")
	}
}

func TestRunJQ_InvalidInputJSON(t *testing.T) {
	code, _ := ingester.CompileJQ(`.x`)
	_, err := ingester.RunJQ(code, []byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON input, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/ingester/... -run TestCompileJQ -v
go test ./internal/ingester/... -run TestRunJQ -v
```

Expected: compilation error — `ingester.CompileJQ` and `ingester.RunJQ` do not exist.

- [ ] **Step 3: Implement internal/ingester/jq.go**

Create `internal/ingester/jq.go`:

```go
package ingester

import (
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// CompileJQ compiles a JQ expression. Returns an error if the expression is invalid.
// Call this once at startup; the returned *gojq.Code is safe for concurrent use.
func CompileJQ(expr string) (*gojq.Code, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("parsing jq expression: %w", err)
	}
	code, err := gojq.Compile(q)
	if err != nil {
		return nil, fmt.Errorf("compiling jq expression: %w", err)
	}
	return code, nil
}

// RunJQ runs a compiled JQ program against raw JSON bytes and returns the resulting events.
// The program may yield a single object or multiple objects (e.g. via .events[]).
// gojq returns an iterator; RunJQ drains it by calling iter.Next() in a loop until (nil, false).
// Each yielded map[string]interface{} is re-marshalled to JSON bytes and passed to ParseJSONLine,
// keeping ParseJSONLine's interface unchanged.
// Returns an error if:
//   - the input is not valid JSON
//   - the iterator yields a JQ runtime error
//   - a yielded value is nil (JQ null)
//   - a yielded value is a non-object type (string, number, etc.)
//   - no values are yielded (empty result)
func RunJQ(code *gojq.Code, rawBytes []byte) ([]Event, error) {
	var input interface{}
	if err := json.Unmarshal(rawBytes, &input); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	iter := code.Run(input)
	var events []Event
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return nil, fmt.Errorf("jq runtime error: %w", err)
		}
		if v == nil {
			return nil, fmt.Errorf("jq produced no output")
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("jq produced unexpected output type: %T", v)
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("jq output marshal error: %w", err)
		}
		evs, err := ParseJSONLine(b)
		if err != nil {
			return nil, err
		}
		events = append(events, evs...)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("jq produced no output")
	}
	return events, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/ingester/... -v
```

Expected: all pass, including all pre-existing ingester tests.

- [ ] **Step 5: Commit**

```bash
git add internal/ingester/jq.go internal/ingester/jq_test.go
git commit -m "feat: add CompileJQ and RunJQ to ingester"
```

---

## Task 4: Wire *gojq.Code through server, handlers, and main

This task updates all wiring in one pass — the signature changes to `server.New`, `SwapEngine`, and `buildFromConfig` affect `server_test.go`, `handlers.go`, and `main.go` simultaneously. All changes must be made before the build will succeed.

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/handlers.go`
- Modify: `internal/server/server_test.go`
- Modify: `cmd/ding/main.go`

- [ ] **Step 1: Write integration tests and update existing server_test.go helpers**

In `internal/server/server_test.go`, make the following changes:

**a) Add import for ingester:**
```go
import (
    // existing imports ...
    "github.com/zuchka/ding/internal/ingester"
)
```

**b) Update all existing `server.New(...)` calls** to pass `nil` as the final `jqCode` argument. There are four locations:
- `makeServer` (line 37): `return server.New(eng, notifiers, cfg, "", nil, nil, nil)`
- `makeServerWithMaxBodyBytes` (line 155): `return server.New(eng, notifiers, cfg, "", nil, nil, nil)`
- `makeServerWithCollector` (line 218): `return server.New(eng, notifiers, cfg, "", c, nil, nil)`
- `TestAlertLog_WritesOnAlert` (line 315): `srv := server.New(eng, notifiers, cfg, "", nil, al, nil)`

**c) Add `makeServerWithJQ` helper:**
```go
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
```

**d) Add integration tests:**
```go
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
```

- [ ] **Step 2: Verify the build fails (expected at this point)**

```bash
go build ./...
```

Expected: compilation errors — `server.New` still takes 6 arguments, `BuildFromConfig` still returns 5 values.

- [ ] **Step 3: Update internal/server/server.go**

**a) Add import for gojq and ingester:**
```go
import (
    // existing imports ...
    "github.com/itchyny/gojq"
    "github.com/zuchka/ding/internal/ingester"
)
```

**b) Add `jqCode` field to `Server` struct:**
```go
type Server struct {
    mu          sync.RWMutex
    bufMu       sync.Mutex  // remove if not present
    engine      *evaluator.Engine
    notifiers   map[string]notifier.Notifier
    cfg         *config.Config
    configPath  string
    mux         *http.ServeMux
    reloadHook  func() error
    alertLogger *notifier.AlertLogger
    collector   *metrics.Collector
    jqCode      *gojq.Code  // nil when no JQ configured
}
```

**c) Update `New` to accept `jqCode`:**
```go
func New(eng *evaluator.Engine, notifiers map[string]notifier.Notifier, cfg *config.Config, configPath string, collector *metrics.Collector, alertLogger *notifier.AlertLogger, jqCode *gojq.Code) *Server {
    s := &Server{
        engine:      eng,
        notifiers:   notifiers,
        cfg:         cfg,
        configPath:  configPath,
        mux:         http.NewServeMux(),
        collector:   collector,
        alertLogger: alertLogger,
        jqCode:      jqCode,
    }
    s.mux.HandleFunc("/health", s.handleHealth)
    s.mux.HandleFunc("/ingest", s.handleIngest)
    s.mux.HandleFunc("/rules", s.handleRules)
    s.mux.HandleFunc("/reload", s.handleReload)
    s.mux.HandleFunc("/metrics", s.handleMetrics)
    return s
}
```

**d) Update `SwapEngine` to accept and store `jqCode`:**
```go
func (s *Server) SwapEngine(eng *evaluator.Engine, cfg *config.Config, notifiers map[string]notifier.Notifier, alertLogger *notifier.AlertLogger, jqCode *gojq.Code) {
    s.mu.Lock()
    oldNotifiers := s.notifiers
    oldLogger := s.alertLogger
    s.engine = eng
    s.cfg = cfg
    s.notifiers = notifiers
    s.alertLogger = alertLogger
    s.jqCode = jqCode
    s.mu.Unlock()

    for _, n := range oldNotifiers {
        if stopper, ok := n.(interface{ Stop() }); ok {
            stopper.Stop()
        }
    }
    if oldLogger != nil {
        if err := oldLogger.Close(); err != nil {
            log.Printf("ding: closing old alert logger: %v", err)
        }
    }
}
```

**e) Update `buildFromConfig` to compile JQ and return it:**

Update the internal signature:
```go
func buildFromConfig(path string, collector *metrics.Collector) (*evaluator.Engine, *config.Config, map[string]notifier.Notifier, *notifier.AlertLogger, *gojq.Code, error) {
```

At the end of the function, before the final return, add JQ compilation:
```go
    var jqCode *gojq.Code
    if cfg.Server.JQ != "" {
        var err error
        jqCode, err = ingester.CompileJQ(cfg.Server.JQ)
        if err != nil {
            for _, n := range notifiers {
                if stopper, ok := n.(interface{ Stop() }); ok {
                    stopper.Stop()
                }
            }
            return nil, nil, nil, nil, nil, fmt.Errorf("compiling jq: %w", err)
        }
    }

    return eng, cfg, notifiers, alertLogger, jqCode, nil
```

Update the exported wrapper to match:
```go
func BuildFromConfig(path string, collector *metrics.Collector) (*evaluator.Engine, *config.Config, map[string]notifier.Notifier, *notifier.AlertLogger, *gojq.Code, error) {
    return buildFromConfig(path, collector)
}
```

**f) Update `IngestLine` to read `jqCode` under lock and use it:**
```go
func (s *Server) IngestLine(line []byte) {
    s.mu.RLock()
    cfg := s.cfg
    eng := s.engine
    notifiers := s.notifiers
    alertLogger := s.alertLogger
    jqCode := s.jqCode
    s.mu.RUnlock()

    var events []ingester.Event
    var err error
    if jqCode != nil {
        events, err = ingester.RunJQ(jqCode, line)
    } else {
        format := ingester.DetectFormat(line, "", cfg.Server.Format)
        if format == "json" {
            events, err = ingester.ParseJSONLine(line)
        } else {
            events, err = ingester.ParsePrometheusText(line)
        }
    }
    if err != nil {
        log.Printf("ding: stdin parse error: %v", err)
        return
    }
    s.processEvents(events, notifiers, eng, alertLogger)
}
```

- [ ] **Step 4: Update internal/server/handlers.go**

**a) Update `handleIngest` to read `jqCode` under the existing lock and use it:**

Inside `handleIngest`, add `jqCode` to the `RLock` block:
```go
s.mu.RLock()
cfg := s.cfg
eng := s.engine
notifiers := s.notifiers
alertLogger := s.alertLogger
jqCode := s.jqCode
s.mu.RUnlock()
```

Then replace the format-detection block:
```go
var events []ingester.Event
var parseErr error
if jqCode != nil {
    events, parseErr = ingester.RunJQ(jqCode, body)
} else {
    format := ingester.DetectFormat(body, r.Header.Get("Content-Type"), cfg.Server.Format)
    if format == "json" {
        events, parseErr = ingester.ParseJSONLine(body)
    } else {
        events, parseErr = ingester.ParsePrometheusText(body)
    }
}
if parseErr != nil {
    jsonError(w, parseErr.Error(), http.StatusBadRequest)
    return
}
```

**b) Update `handleReload` inline path (lines 127-135) — the 5th call site:**

```go
// Default inline reload (no persistence awareness).
newEng, newCfg, newNotifiers, newAlertLogger, newJQCode, err := buildFromConfig(s.configPath, s.collector)
if err != nil {
    jsonError(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
    return
}
s.SwapEngine(newEng, newCfg, newNotifiers, newAlertLogger, newJQCode)
```

- [ ] **Step 5: Update cmd/ding/main.go — 4 call sites**

**a) `runValidate`** — discard the jqCode (just need the error):
```go
func runValidate(configPath string) error {
    _, _, _, _, _, err := server.BuildFromConfig(configPath, nil)
    if err != nil {
        return fmt.Errorf("config invalid: %w", err)
    }
    fmt.Println("config OK:", configPath)
    return nil
}
```

**b) `runServe` — initial startup:**
```go
eng, cfg, notifiers, alertLogger, jqCode, err := server.BuildFromConfig(configPath, collector)
if err != nil {
    return fmt.Errorf("loading config: %w", err)
}

srv := server.New(eng, notifiers, cfg, configPath, collector, alertLogger, jqCode)
```

**c) `runServe` — reload hook closure** (two variables: `newJQCode`):
```go
srv.SetReloadHook(func() error {
    reloadMu.Lock()
    defer reloadMu.Unlock()

    stopFlusher()
    stopFlusher = func() {}

    newEng, newCfg, newNotifiers, newAlertLogger, newJQCode, err := server.BuildFromConfig(configPath, collector)
    if err != nil {
        if cfg.Persistence.StateFile != "" {
            stopFlusher = eng.StartFlusher(cfg.Persistence.StateFile, cfg.Persistence.FlushInterval.Duration)
        }
        return fmt.Errorf("reload failed: %w", err)
    }

    if newCfg.Persistence.StateFile != "" {
        snap, err := evaluator.LoadSnapshot(newCfg.Persistence.StateFile)
        if err != nil {
            log.Printf("ding: state restore after reload failed: %v (new engine starts fresh)", err)
        } else if snap != nil {
            evaluator.RestoreEngine(newEng, *snap, time.Now())
        }
    }

    srv.SwapEngine(newEng, newCfg, newNotifiers, newAlertLogger, newJQCode)
    eng = newEng
    cfg = newCfg
    notifiers = newNotifiers
    alertLogger = newAlertLogger
    log.Printf("ding: config reloaded from %s", configPath)

    if newCfg.Persistence.StateFile != "" {
        stopFlusher = newEng.StartFlusher(newCfg.Persistence.StateFile, newCfg.Persistence.FlushInterval.Duration)
    }
    return nil
})
```

**d) `runServe` — SIGHUP goroutine** (same pattern as reload hook):
```go
newEng, newCfg, newNotifiers, newAlertLogger, newJQCode, err := server.BuildFromConfig(configPath, collector)
// ... error handling unchanged ...
srv.SwapEngine(newEng, newCfg, newNotifiers, newAlertLogger, newJQCode)
```

- [ ] **Step 6: Verify the build succeeds**

```bash
go build ./...
```

Expected: clean build, no errors.

- [ ] **Step 7: Run all tests**

```bash
go test ./...
```

Expected: all pass, including the new integration tests.

- [ ] **Step 8: Commit**

```bash
git add internal/server/server.go internal/server/handlers.go internal/server/server_test.go cmd/ding/main.go
git commit -m "feat: wire JQ inbound transform through server and handlers"
```

---

## Task 5: Update ding.yaml.example and verify ding validate

**Files:**
- Modify: `ding.yaml.example`

- [ ] **Step 1: Add JQ example to ding.yaml.example**

Add a commented-out example block after the `format` line in the server section:

```yaml
  # JQ transform (optional): transform arbitrary inbound JSON before parsing.
  # The expression must produce an object or array of objects, each with
  # "metric" (string) and "value" (number) fields. Extra fields become labels.
  # Example: accept Datadog-style batched events:
  # jq: '.series[] | {metric: .metric, value: (.points[0][1]), host: .tags[0]}'
```

- [ ] **Step 2: Verify ding validate catches a bad JQ expression**

Create a temp config with an invalid JQ field and run validate:

```bash
cat > /tmp/bad-jq.yaml << 'EOF'
server:
  jq: 'not valid jq |||'
rules:
  - name: test
    condition: "value > 0"
    alert:
      - notifier: stdout
EOF
go run ./cmd/ding validate --config /tmp/bad-jq.yaml
```

Expected: non-zero exit with error message containing "compiling jq".

- [ ] **Step 3: Verify ding validate passes with a valid JQ expression**

```bash
cat > /tmp/good-jq.yaml << 'EOF'
server:
  jq: '.events[] | {metric: .name, value: .v}'
rules:
  - name: test
    condition: "value > 0"
    alert:
      - notifier: stdout
EOF
go run ./cmd/ding validate --config /tmp/good-jq.yaml
```

Expected: `config OK: /tmp/good-jq.yaml`

- [ ] **Step 4: Run the full test suite one final time**

```bash
go test ./...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add ding.yaml.example
git commit -m "docs: add jq example to ding.yaml.example"
```
