# DING

> Don't store it. Stream it. DING it.

```
$ brew install zuchka/tap/ding
```

```
$ curl -sf https://start.ding.ing | sh
```

[Docker, binary](#install)

---

## What

DING is a stream-based alerting daemon. Pipe metrics into it. It evaluates rules. It fires alerts. That's it.

Single binary. No database. No agents. No cloud account. Works anywhere.

---

## Quickstart

1. **Copy the example config and validate it**

   ```
   cp ding.yaml.example ding.yaml
   ding validate        # exits 0 if valid, 1 with a clear error if not
   ```

2. **Start DING**

   ```
   ding serve

   # point at a specific config
   ding serve --config /etc/myapp/alerts.yaml

   # download and start in one shot
   curl -sf https://start.ding.ing | sh && ding serve --config ./ding.yaml
   ```

3. **Send an event**

   ```
   curl -X POST http://localhost:8080/ingest \
     -H "Content-Type: application/json" \
     -d '{"metric":"cpu_usage","value":97,"host":"web-01"}'
   ```

4. **Or just pipe**

   ```
   your-app | ding serve
   ```

---

## Rules

One YAML file. Lives in your repo. Ships with your code.

```yaml
rules:
  # fires on a single bad reading
  - name: cpu_spike
    match:
      metric: cpu_usage
    condition: value > 95
    cooldown: 1m
    message: "CPU spike on {{ .host }}: {{ .value }}%"
    alert:
      - notifier: stdout

  # fires on sustained high CPU (windowed)
  - name: cpu_sustained
    match:
      metric: cpu_usage
    condition: avg(value) over 5m > 80
    cooldown: 10m
    message: "Sustained high CPU: {{ .avg }}% avg on {{ .host }}"
    alert:
      - notifier: stdout
```

All condition forms:

```
value > 95                       # single event
avg(value) over 5m > 80         # average over window
max(value) over 1m >= 100
min(value) over 10s < 10
sum(value) over 30s > 0
count(value) over 2m > 50       # number of events, not sum of values
```

Template variables in `message:`:

| Variable | When | Description |
|----------|------|-------------|
| `.metric` | always | metric name |
| `.value` | always | raw event value |
| `.rule` | always | rule name |
| `.fired_at` | always | RFC3339 timestamp |
| `.host`, `.region`, … | always | any label from the event |
| `.avg` `.max` `.min` `.sum` `.count` | windowed only | aggregate result |

---

## Notifiers

`stdout` is built-in. For webhooks, define them once and reference by name.

```yaml
notifiers:
  alert-slack:
    type: webhook
    url: https://hooks.slack.com/services/T.../B.../...
    max_attempts: 3       # retries on 5xx (default: 3)
    initial_backoff: 1s   # doubles each attempt (default: 1s)

rules:
  - name: cpu_spike
    condition: value > 95
    cooldown: 1m
    alert:
      - notifier: stdout        # always available, no declaration needed
      - notifier: alert-slack   # send to both simultaneously
```

The webhook receives a JSON POST:

```json
{"rule":"cpu_spike","message":"CPU spike on web-01: 97%",
 "metric":"cpu_usage","value":97.0,"fired_at":"...","host":"web-01"}
```

4xx responses are dropped. 5xx responses are retried with exponential backoff.

---

## Why

> **Fires alerts in 4ms.** Prometheus default scrape + eval + Alertmanager dispatch: ~62 seconds minimum. That's not a knock on Prometheus — it's a pull-based system built for persistence and fleet-wide aggregation. DING is push-based and stateless. The architecture is the difference.

- **Zero infrastructure** — no Prometheus, no Alertmanager, no storage, no agents
- **Windowed aggregations** — `avg(value) over 5m` works with no database, just memory
- **Per-host cooldowns** — `web-01` being loud doesn't silence `web-02`
- **Composable** — stdin in, JSON lines out, pipes into anything
- **Config in your repo** — 12 lines, 1 file vs 30 lines across 3 files for the Prometheus equivalent. Alerting is a dev artifact, not an ops artifact.
- **5MB static binary, 9ms cold start** — runs on linux/arm64, amd64, macOS, Windows. Prometheus cold start: 185ms.

---

## Performance

| Metric | Result | Context |
|--------|--------|---------|
| Alert latency p50 | **4ms** | p99: 16ms — Prometheus default: ~62s |
| Requests / second | **116k** | 50 concurrent workers, 30s window |
| Cold start p50 | **9ms** | fork → first /health — Prometheus: 185ms |
| Per rule evaluation | **106ns** | simple threshold — windowed: 157ns |

Benchmarked 2026-03-23 on Apple M3. [Full methodology and raw results →](https://github.com/zuchka/ding/blob/main/BENCHMARKS.md)

---

## Input

JSON lines:

```json
{"metric": "cpu_usage", "value": 92.5, "host": "web-01"}
```

Prometheus text:

```
cpu_usage{host="web-01"} 92.5
```

---

## HTTP API

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/ingest` | Send events |
| `GET` | `/health` | Health check |
| `GET` | `/rules` | List rules + cooldown state |
| `POST` | `/reload` | Hot-reload config |

---

## Operations

Reload config without restarting:

```
kill -HUP <pid>
# or
curl -X POST http://localhost:8080/reload
```

Survive restarts — persist cooldown state and windowed buffers to disk:

```yaml
persistence:
  state_file: /var/lib/ding/state.json
  flush_interval: 30s
```

SIGTERM / SIGINT — drains in-flight requests, flushes state, exits 0.

---

## Install

**Homebrew:**

```
brew install zuchka/tap/ding
```

**Binary:**

```
curl -sf https://start.ding.ing | sh
```

**Docker:**

```
docker run -v ./ding.yaml:/etc/ding/ding.yaml \
  ghcr.io/zuchka/ding
```

---

MIT license · [ding.ing](https://ding.ing)
