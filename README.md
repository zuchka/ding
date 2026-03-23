# DING

> Don't store it. Stream it. DING it.

DING is a stream-based alerting daemon — a single binary that listens for metrics/events (via HTTP or stdin), evaluates rules, and fires alerts when conditions are met. No storage. No infrastructure. Works anywhere.

## Install

**Homebrew:**
```
brew install zuchka/tap/ding
```

**Binary download:**
```
curl -sf https://install.ding.ing | sh
```

**Docker:**
```
docker run -v ./ding.yaml:/etc/ding/ding.yaml ghcr.io/zuchka/ding
```

## Quickstart

1. Copy the example config:
   ```
   cp ding.yaml.example ding.yaml
   ```

2. Start DING:
   ```
   ding serve
   ```

3. Send an event:
   ```
   curl -X POST http://localhost:8080/ingest \
     -H "Content-Type: application/json" \
     -d '{"metric":"cpu_usage","value":97,"host":"web-01"}'
   ```

4. Pipe data:
   ```
   your-app | ding serve
   ```

## Rule Syntax

```yaml
rules:
  # Event-per-event
  - name: cpu_spike
    match:
      metric: cpu_usage
    condition: value > 95
    cooldown: 1m
    message: "CPU spike on {{ .host }}: {{ .value }}%"
    alert:
      - notifier: stdout

  # Windowed aggregation
  - name: cpu_sustained
    match:
      metric: cpu_usage
    condition: avg(value) over 5m > 80
    cooldown: 10m
    alert:
      - notifier: stdout
```

## Input Formats

**JSON lines:**
```json
{"metric": "cpu_usage", "value": 92.5, "host": "web-01"}
```

**Prometheus text:**
```
cpu_usage{host="web-01"} 92.5
```

## HTTP API

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/ingest` | Send events |
| `GET` | `/health` | Health check |
| `GET` | `/rules` | List rules + cooldown state |
| `POST` | `/reload` | Hot-reload config |

## Signals

- `SIGHUP` — reload config
- `SIGTERM` / `SIGINT` — graceful shutdown

## License

MIT
