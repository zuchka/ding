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
POST /ingest
  ↓
  if server.jq is configured:
    run jq(raw body) → one object OR array of objects
    each object → ParseJSONLine → Event
    any JQ error, null output, or missing required field → 400
  else:
    current behavior (ParseJSONLine / ParsePrometheus)
  ↓
  engine.Process() — unchanged
```

The JQ expression is compiled **once at startup**. An invalid expression is a fatal error — ding refuses to start.

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

---

## Components

### `internal/config/config.go`
Add `JQ string \`yaml:"jq"\`` to `ServerConfig`. No validation here — JQ compilation belongs in server startup to avoid bleeding the `gojq` dependency into the config package.

### `internal/ingester/jq.go` (new file)
Owns the JQ concern:
- `CompileJQ(expr string) (*gojq.Code, error)` — compiles at startup
- `RunJQ(code *gojq.Code, rawBytes []byte) ([]Event, error)` — runs the program against raw bytes, handles single-object and array output, delegates each result object to `ParseJSONLine`

### `internal/server/handlers.go`
Minimal change: if a compiled JQ program is present on the server, call `RunJQ` instead of `ParseJSONLine`/`ParsePrometheus`. On error, return 400 with the JQ error message.

### New dependency
`github.com/itchyny/gojq` — pure-Go JQ implementation, no CGO.

---

## Error Handling

| Scenario | Behavior |
|---|---|
| Invalid JQ expression at startup | `log.Fatalf` — ding won't start |
| JQ runtime error | HTTP 400, error message in body |
| JQ returns `null` or empty array | HTTP 400, `"jq produced no output"` |
| JQ output missing `metric` or `value` | HTTP 400 (existing `ParseJSONLine` validation) |
| No `jq` configured | Zero behavior change |

---

## Testing

### `internal/ingester/jq_test.go` (new)
- Valid expression, single object → one Event
- Valid expression, array output → multiple Events
- JQ runtime type error → error returned
- JQ returns null → error returned
- Output missing `metric` → error returned
- Output missing `value` → error returned

### `internal/server/server_test.go`
- POST with `jq` configured → events processed and alert fires (integration)
