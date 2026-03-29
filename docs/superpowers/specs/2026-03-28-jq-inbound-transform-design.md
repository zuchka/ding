# JQ Inbound Transform — Design Spec

**Date:** 2026-03-28
**Status:** Approved

---

## Overview

Add an optional JQ expression to the `server` config block that transforms arbitrary inbound JSON payloads into ding's internal event format before evaluation. This allows ding to accept webhook payloads from external systems (Datadog, GitHub, custom apps) without requiring the caller to reshape their payload.

---

## Architecture & Data Flow

JQ is a preprocessing step that runs before the existing ingester. The engine, rules, conditions, cooldowns, and notifiers are untouched.

```
POST /ingest  (or stdin via IngestLine)
  ↓
  if server.jq is configured:
    run jq(raw body) → one object OR array of objects
    each object → ParseJSONLine → Event
    any JQ error, non-object output, null output, or missing required field → 400 / log+skip
  else:
    current behavior (ParseJSONLine / ParsePrometheus)
  ↓
  engine.Process() — unchanged
```

The JQ expression is compiled **once per config load** inside `buildFromConfig`. An invalid expression causes `buildFromConfig` to return an error — ding refuses to start (or reload), and `ding validate` catches it automatically.

---

## Config

One new optional field in the `server` block:

```yaml
server:
  port: 8080
  format: json
  jq: '.events[] | {metric: .name, value: .reading, host: .tags.host}'
```

If `jq` is absent, behavior is identical to today. No performance cost.

**Interaction with `format`:** When `jq` is configured, the `format` field is ignored for inbound parsing — JQ output always feeds into `ParseJSONLine`. The `format` field remains valid in config and is not an error, but has no effect on the transform path.

---

## Components

### `internal/config/config.go`
Add `JQ string \`yaml:"jq"\`` to `ServerConfig`. No JQ compilation here — the config package does not import `gojq`.

### `internal/ingester/jq.go` (new file)
Owns the JQ concern:
- `CompileJQ(expr string) (*gojq.Code, error)` — compiles at startup
- `RunJQ(code *gojq.Code, rawBytes []byte) ([]Event, error)` — runs the program against raw bytes, handles single-object and array output, delegates each result object to `ParseJSONLine`. Returns an error if JQ produces null, an empty array, or a non-object value (bare string, number, etc.) — distinguishing this "shape error" from a JQ runtime error with a clear message like `"jq produced unexpected output type: string"`.

### `internal/server/server.go`
- Add `jqCode *gojq.Code` field to `Server` struct (nil when no JQ configured)
- Update `New(...)` to accept `*gojq.Code` as a parameter
- Update `SwapEngine(...)` to accept and store `*gojq.Code` alongside the new engine

### `internal/server/handlers.go`
- `handleIngest`: if `s.jqCode != nil`, call `ingester.RunJQ` instead of `ParseJSONLine`/`ParsePrometheus`. On error, return 400 with the error message.
- `IngestLine`: same check — if `s.jqCode != nil`, call `ingester.RunJQ`. On error, log and skip (matches existing stdin error behavior).

### `internal/server/buildFromConfig` (internal function in server.go)
Compile JQ here, after loading config:
```go
var jqCode *gojq.Code
if cfg.Server.JQ != "" {
    jqCode, err = ingester.CompileJQ(cfg.Server.JQ)
    if err != nil {
        return nil, nil, nil, nil, nil, fmt.Errorf("compiling jq: %w", err)
    }
}
```
Update return signature to include `*gojq.Code`:
```go
func buildFromConfig(path string, collector *metrics.Collector) (*evaluator.Engine, *config.Config, map[string]notifier.Notifier, *notifier.AlertLogger, *gojq.Code, error)
```
`BuildFromConfig` (exported) is updated to match. All callers in `main.go` and `runValidate` receive the compiled code and pass it to `New` or `SwapEngine`.

### `cmd/ding/main.go`
All four call sites of `BuildFromConfig` / `buildFromConfig` are updated to receive `*gojq.Code` and thread it through `server.New` and `srv.SwapEngine`. No logic changes — mechanical wiring only.

### New dependency
`github.com/itchyny/gojq` — pure-Go JQ implementation, no CGO.

---

## Hot-Reload Behavior

Since JQ compilation happens inside `buildFromConfig`, hot-reload via SIGHUP or `POST /reload` automatically recompiles the JQ expression from the new config. If the new expression is invalid, `buildFromConfig` returns an error and the reload fails — ding keeps the current config, exactly as it does today for bad rule conditions. If the new config removes `jq`, `SwapEngine` stores `nil` and JQ is disabled immediately.

---

## `ding validate`

`runValidate` calls `BuildFromConfig(configPath, nil)`. Since JQ compilation now lives inside `buildFromConfig`, an invalid `jq` expression is caught by `ding validate` and reported as a config error. No changes to `runValidate` needed.

---

## Error Handling

| Scenario | Behavior |
|---|---|
| Invalid JQ expression at startup | `buildFromConfig` returns error — ding won't start (or reload) |
| Invalid JQ expression on `ding validate` | Reported as config error |
| JQ runtime error | HTTP ingest: 400 with error message; stdin: log and skip |
| JQ returns `null` or empty array | HTTP ingest: 400 `"jq produced no output"`; stdin: log and skip |
| JQ returns non-object (string, number) | HTTP ingest: 400 `"jq produced unexpected output type: <type>"`; stdin: log and skip |
| JQ output missing `metric` or `value` | HTTP ingest: 400 (existing `ParseJSONLine` validation); stdin: log and skip |
| No `jq` configured | Zero behavior change |
| `jq` configured + `format: prometheus` | `format` ignored; JQ path always uses `ParseJSONLine` |

**Fan-out and `MaxBodyBytes`:** `MaxBodyBytes` is enforced before JQ runs, bounding the input. JQ can fan out a single payload into many events (e.g. `.events[]`); there is no per-output limit. This is acceptable for v1.

---

## Testing

### `internal/ingester/jq_test.go` (new)
- Valid expression, single object → one Event
- Valid expression, array output → multiple Events
- JQ runtime error (type error) → error returned with clear message
- JQ returns null → error returned
- JQ returns bare string → error with `"unexpected output type: string"`
- Output missing `metric` → error returned
- Output missing `value` → error returned

### `internal/server/server_test.go`
- Integration test: construct server with compiled JQ via `ingester.CompileJQ` + `server.New(..., jqCode)`. POST a payload that requires JQ to extract fields → event processed, alert fires.
- Hot-reload test: verify `SwapEngine` with a new `*gojq.Code` takes effect on subsequent requests.
