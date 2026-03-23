# DING — Product Design Specification

**Date:** 2026-03-22
**Status:** Approved

---

## Overview

DING is a stream-based alerting daemon. A single Go binary that listens for a stream of metrics/events (via HTTP POST or stdin), evaluates user-defined YAML rules, and fires alerts when conditions are met. No storage. No heavy infrastructure. Drop it on any server and it works.

**Tagline:** *Don't store it. Stream it. DING it.*

---

## Problem

Existing alerting tools (Alertmanager, Prometheus, Grafana) are operationally heavy, require persistent storage, and take significant effort to set up. Developers need something that:

- Runs anywhere as a single binary (POSIX-friendly, no runtime dependencies)
- Can be running in under a minute
- Lives next to their application code
- Speaks common formats (JSON, Prometheus)
- Integrates with tools they already use (Slack, PagerDuty, webhooks)

DING fills this gap.

---

## Requirements

### Hard Requirements
- Open source (MIT license)
- Developer focused
- Single static binary — no runtime dependencies, runs anywhere
- Makes intuitive use of the name DING (alert fires → it DINGs you)

### Core Capabilities (v1)
- Accept input via HTTP POST and/or stdin pipe (both simultaneously)
- Parse JSON lines and Prometheus text exposition format
- Evaluate alert rules defined in `ding.yaml`
- Hybrid rule evaluation: event-per-event thresholds AND windowed aggregations
- Fire alerts via webhooks (any HTTP endpoint) and stdout
- Per-rule cooldown periods (per label-set) to prevent alert storms
- Long-running daemon mode with hot-reload (SIGHUP or POST /reload)
- Graceful shutdown on SIGTERM/SIGINT

### Out of Scope (v1)
- Persistent storage of any kind
- Built-in dashboards or visualization
- Metrics collection / scraping
- SMS / Telegram / email / PagerDuty native notifications (v2)
- Plugin architecture (v2)
- Compound conditions (`AND`, `OR`) in rules (v2)
- Retry logic for failed webhook deliveries (v2)

---

## Architecture

```
[Data Source]  ──stdin──►  ┌─────────────────────────────┐
                            │           DING              │
[App / Script] ──HTTP──►   │                             │
                            │  1. Parse (JSON / Prom fmt) │
[curl / agent] ──HTTP──►   │  2. Match rules             │  ──► webhook (any HTTP endpoint)
                            │  3. Evaluate conditions     │  ──► stdout
                            │  4. Check cooldowns         │
                            │  5. Fire alerts             │
                            └─────────────────────────────┘
                                      ▲
                                 ding.yaml
                          (rules live in your repo)
```

### Key Properties
- **Language:** Go — single static binary for any OS/arch
- **State:** In-memory only — cooldown timers and ring buffers for windowed rules
- **Hot-reload:** `POST /reload` or `SIGHUP` swaps engine under `sync.RWMutex` (write lock on swap, read lock during evaluation; in-flight evaluations complete before swap)
- **Image size:** ~5MB Docker image (scratch-based)
- **Platforms:** linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64

---

## Input Modes

Both modes operate simultaneously when `ding serve` starts.

**HTTP mode:** listens on configured port; events POSTed to `/ingest`.

**Stdin mode:** if stdin is a pipe (not a TTY), reads events line-by-line from stdin concurrently with the HTTP server. When stdin reaches EOF, the HTTP server continues running. Stdin is checked via `os.Stdin.Stat()` using `ModeCharDevice`.

---

## Input Formats

### JSON Lines
One JSON object per line. The `metric` key is required. `value` must be a number. All other keys become label fields available in match and message templates.

```json
{"metric": "cpu_usage", "value": 92.5, "host": "web-01"}
{"metric": "http_errors", "value": 5, "host": "api-02", "region": "us-east"}
```

### Prometheus Text Format
Standard Prometheus exposition format. Each time series becomes one `Event`.

```
# HELP cpu_usage Current CPU usage percentage
# TYPE cpu_usage gauge
cpu_usage{host="web-01"} 92.5
http_errors{host="api-02",region="us-east"} 5
```

### Format Selection

The `server.format` config field controls parsing:

| Value | Behavior |
|-------|----------|
| `json` | Always parse as JSON lines |
| `prometheus` | Always parse as Prometheus text |
| `auto` | For HTTP: use `Content-Type` header if present (`application/json` → JSON, `text/plain` → Prometheus); otherwise attempt JSON first (check if first non-whitespace character is `{`), fall back to Prometheus. For stdin: same heuristic per line. |

---

## Rule Specification

### Match Block

The `match` block filters which events a rule applies to. All key-value pairs must match (exact string equality). The `metric` key matches the event's metric name. Any other key matches a label.

```yaml
match:
  metric: cpu_usage          # required: match metric name
  host: web-01               # optional: also match this label value
  region: us-east            # optional: and this label value
```

If `match` is omitted, the rule applies to all events.

Wildcard and regex matching are **not supported in v1**.

### Condition Grammar

Conditions are single expressions in one of two forms:

**Event-per-event (no `over` clause):**
```
value OP literal
```

**Windowed (with `over` clause):**
```
FUNC(value) over DURATION OP literal
```

Where:
- `OP` is one of: `>`, `>=`, `<`, `<=`, `==`, `!=`
- `literal` is a number (integer or float)
- `DURATION` is a Go duration string: `30s`, `5m`, `1h`
- `FUNC` is one of: `avg`, `max`, `min`, `count`, `sum`

Examples:
```
value > 95
value <= 0
avg(value) over 5m > 80
max(value) over 1m >= 100
count(value) over 2m > 50
```

Compound conditions (`AND`, `OR`) are not supported in v1.

> **Note on `count`:** `count(value)` counts the number of events in the window — the `value` argument is ignored. This is distinct from summing a field that itself represents a count. Use `sum(value)` if you want to aggregate a field that is itself a count (e.g. `http_errors` where each event carries a batch count).

### Windowed Rule Semantics

- **Window type:** wall-clock time-based (events are timestamped at ingestion time)
- **Event timestamp:** ingestion wall clock. A `timestamp` field in JSON payload (Unix seconds float) can override the time used for ring buffer window eviction. `timestamp` is a reserved field — it is **not** available as a label in `match` blocks or as a template variable.
- **`.fired_at`** always reflects ingestion wall-clock time regardless of any `timestamp` override.
- **Ring buffer key:** `(rule_name, sorted_label_key_value_pairs)` — each unique label combination maintains its own independent ring buffer. For the `/rules` API response, label-sets are serialized as a comma-separated string of `key=value` pairs sorted alphabetically by key (e.g. `"host=web-01,region=us-east"`).
- **Eviction:** events older than the window duration are evicted before each evaluation
- **Maximum buffer size:** 10,000 events per (rule, label-set) combination (prevents unbounded memory growth under high-frequency input). Configurable via `server.max_buffer_size`.
- **Empty buffer:** if the ring buffer contains no events, the windowed condition evaluates to `false` (no alert fires)
- **Out-of-order events:** events are inserted at ingestion time; out-of-order events based on `timestamp` field are still inserted at their ingestion position (no reordering in v1)

### Cooldown Semantics

- **Scope:** per (rule, label-set) combination — `cpu_spike` firing for `host=web-01` does not block it from firing for `host=web-02`
- **Trigger:** level-triggered — fires once when condition becomes true, then enters cooldown. After cooldown expires, fires again on the next violation
- **Reset:** cooldown timer does not reset on repeated violations; it runs to completion

### Message Templates

Go `text/template` syntax. Available variables:

| Variable | Always available | Notes |
|----------|-----------------|-------|
| `.metric` | Yes | Metric name |
| `.value` | Yes | Raw event value (float64) |
| `.rule` | Yes | Rule name |
| `.fired_at` | Yes | RFC3339 timestamp of alert fire |
| `.<label>` | Yes | Any label from the matched event (e.g. `.host`, `.region`) |
| `.avg` | Windowed rules only | Result of `avg()` aggregate |
| `.max` | Windowed rules only | Result of `max()` aggregate |
| `.min` | Windowed rules only | Result of `min()` aggregate |
| `.count` | Windowed rules only | Result of `count()` aggregate |
| `.sum` | Windowed rules only | Result of `sum()` aggregate |

---

## Configuration (ding.yaml)

```yaml
server:
  port: 8080
  format: json              # json | prometheus | auto
  max_buffer_size: 10000    # max events per windowed ring buffer

notifiers:
  alert-slack:
    type: webhook
    url: https://hooks.slack.com/T.../...
  alert-pagerduty:
    type: webhook
    url: https://your-proxy-or-pd-webhook/...
    # Note: PagerDuty Events API v2 requires a routing_key and structured payload.
    # In v1, use a webhook proxy (e.g. Alertmanager, custom Lambda) that accepts
    # DING's generic payload and forwards to PagerDuty. Native PagerDuty support
    # with routing_key is planned for v2.

rules:

  # Event-per-event: fires on a single bad reading
  - name: cpu_spike
    match:
      metric: cpu_usage
    condition: value > 95
    cooldown: 1m
    message: "CPU spike on {{ .host }}: {{ .value }}%"
    alert:
      - notifier: alert-slack

  # Windowed: fires only on sustained high CPU
  - name: cpu_sustained
    match:
      metric: cpu_usage
    condition: avg(value) over 5m > 80
    cooldown: 10m
    message: "Sustained high CPU on {{ .host }}: avg {{ .avg }}%"
    alert:
      - notifier: alert-pagerduty
      - notifier: stdout

  # Composable: pipe alerts downstream
  - name: error_rate
    match:
      metric: http_errors
    condition: count(value) over 1m > 100
    cooldown: 5m
    alert:
      - notifier: stdout
```

### Built-in Notifiers

`stdout` is a built-in notifier type. It does not need to be declared in `notifiers:` — reference it directly in any rule's alert block:

```yaml
alert:
  - notifier: stdout
```

Stdout output format (one JSON object per line):
```json
{"rule":"cpu_spike","message":"CPU spike on web-01: 97%","metric":"cpu_usage","value":97,"host":"web-01","fired_at":"2026-03-22T14:30:00Z"}
```

### Webhook Notifier

Configured under `notifiers:` with `type: webhook` and `url`. DING sends an HTTP POST with `Content-Type: application/json` and the same payload shape as stdout output. On delivery failure: logs error to stderr and drops the alert (no retry in v1).

---

## HTTP API

### `POST /ingest`

Accept events. Request body: JSON lines or Prometheus text (see Format Selection).

Success response `200 OK`:
```json
{"events": 3, "alerts_fired": 1}
```

Error response `400 Bad Request`:
```json
{"error": "failed to parse line 2: invalid JSON"}
```

### `GET /health`

Returns `200 OK` with body `{"status": "ok"}`.

### `GET /rules`

Returns active rules with cooldown state:

```json
[
  {
    "name": "cpu_spike",
    "condition": "value > 95",
    "cooldown": "1m",
    "cooling_down": {
      "host=web-01": "42s remaining",
      "host=web-01,region=us-east": "ready"
    }
  }
]
```

### `POST /reload`

Hot-reloads `ding.yaml`. Acquires a write lock, swaps the engine, releases lock. In-flight evaluations complete first. Returns `200 OK` on success, `500` with error body on config parse failure (old config remains active).

---

## Signal Handling

| Signal | Action |
|--------|--------|
| `SIGHUP` | Hot-reload ding.yaml (same as POST /reload) |
| `SIGTERM` | Graceful shutdown: drain in-flight requests (max 30s timeout), then exit 0 |
| `SIGINT` | Graceful shutdown (same as SIGTERM) |

---

## CLI Subcommands

| Command | Description |
|---------|-------------|
| `ding serve` | Start the daemon (HTTP server + optional stdin reader) |
| `ding serve --config path/to/ding.yaml` | Use a specific config file (default: `./ding.yaml`) |
| `ding validate` | Parse and validate config (default: `./ding.yaml`), report errors, exit 0 if valid |
| `ding version` | Print version string |

---

## Project Structure

```
zuchka/
├── cmd/ding/main.go          # CLI entry point (cobra)
├── internal/
│   ├── config/               # YAML config loading and validation
│   ├── ingester/             # Event parsing: JSON lines + Prometheus text
│   ├── evaluator/            # Rule engine: matching, conditions, ring buffers, cooldowns
│   ├── notifier/             # Alert dispatch: webhook, stdout
│   └── server/               # HTTP server and request routing
├── ding.yaml.example         # Starter configuration
├── go.mod
├── Dockerfile
├── install.sh
└── .github/workflows/
    └── release.yml           # GoReleaser cross-platform builds
```

---

## Distribution

| Channel | How |
|---------|-----|
| GitHub Releases | GoReleaser; binaries for all platforms |
| Homebrew | `brew install zuchka/tap/ding` |
| Docker | `docker run -v ./ding.yaml:/etc/ding/ding.yaml ghcr.io/zuchka/ding` |
| Install script | `curl -sf https://start.ding.ing \| sh` |

---

## Verification

### Unit Tests (`go test ./...`)
- Config: load and validate `ding.yaml`; test invalid configs produce clear errors
- Ingester: parse JSON lines; parse Prometheus text format; format auto-detection
- Evaluator: event-per-event conditions; windowed aggregations; ring buffer eviction; cooldowns per label-set; empty buffer evaluates false
- Notifier: webhook dispatch (mock HTTP server); stdout JSON formatting

### Integration Smoke Test
```bash
# Start DING
ding serve --config ding.yaml.example

# Ingest an event that crosses threshold
curl -X POST http://localhost:8080/ingest \
  -H "Content-Type: application/json" \
  -d '{"metric":"cpu_usage","value":97,"host":"test-01"}'

# Expected: 200 {"events":1,"alerts_fired":1}
# Expected: alert appears on stdout
```

### Stdin Pipe Test
```bash
echo '{"metric":"cpu_usage","value":97,"host":"test-01"}' | ding serve --config ding.yaml.example
# Alert appears on stdout; HTTP server continues running
```

### Hot-Reload Test
```bash
# Start ding, modify ding.yaml, then:
curl -X POST http://localhost:8080/reload
# Returns 200; new rules active without restart
# Alternatively: kill -HUP <ding-pid>
```

### Validate Command Test
```bash
ding validate --config ding.yaml.example   # exits 0
ding validate --config /nonexistent.yaml   # exits 1 with error message
```
