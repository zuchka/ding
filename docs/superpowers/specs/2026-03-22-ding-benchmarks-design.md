# Ding Benchmark Suite Design

**Date:** 2026-03-22
**Status:** Draft

## Context

Ding positions itself between fragile shell scripts and heavyweight observability platforms (Prometheus + Alertmanager, Datadog). Its core design decisions — zero storage, bounded ring buffers, single static binary, in-process evaluation — make strong implicit performance claims. This benchmark suite makes those claims explicit and reproducible.

The goal is internal validation: can Ding actually deliver on its positioning against the two most relevant competitors? Numbers should be runnable and trustworthy, not marketing copy.

Primary competitors:
- **Prometheus + Alertmanager** — canonical OSS stack, most relevant direct comparison
- **Datadog streaming alerting** — SaaS incumbent, DogStatsD + cloud-side evaluation

---

## Test Environment

| Item | Version / Spec |
|------|---------------|
| Hardware | Apple M1 Pro, 16 GB RAM |
| OS | macOS 14 (Sonoma) |
| Go | 1.22 |
| Docker Desktop | 4.x |
| Prometheus | v2.51 |
| Alertmanager | v0.27 |
| Datadog Agent | v7 (DogStatsD + metrics API) |

All benchmarks run locally. Ding's Go benchmarks run natively. Competitor benchmarks run in Docker Compose.

---

## Benchmark 1: Alert Latency

**What it measures:** Time from event ingestion to alert firing — condition met → webhook POST received.

**Methodology:**
- Single rule: `value > 95`, event-per-event (no window)
- POST one event with `value=97` to each system
- A local webhook receiver records receipt timestamp; subtract ingest timestamp
- 100 runs, report p50 and p99
- Prometheus tested twice: **default** settings and **minimum-latency** settings
- Datadog: metric delivered via DogStatsD UDP (`echo "cpu_usage:97|g" | nc -u -w1 127.0.0.1 8125`); alert monitor pre-created via Datadog API before the run; cloud-side evaluation latency measured from DogStatsD send time to webhook receipt

Prometheus minimum-latency config:
```yaml
# prometheus.yml
global:
  scrape_interval: 1s
  evaluation_interval: 1s

# alertmanager.yml
route:
  group_wait: 0s
  group_interval: 1s
  repeat_interval: 1h
```
(alert rule also removes `for:` duration)

**Expected results:**

| System | p50 | p99 |
|--------|-----|-----|
| Ding | 2 ms | 8 ms |
| Prometheus + AM (minimum settings) | 2,100 ms | 4,300 ms |
| Prometheus + AM (default settings) | 62,000 ms | 91,000 ms |
| Datadog (default collection interval) | 45,000 ms | 180,000 ms |

**Why Ding wins:** In-process evaluation. Parse → eval → dispatch happens synchronously in one goroutine with no inter-process hops. Prometheus must complete a full scrape cycle before evaluation can run; Alertmanager adds routing and group-wait overhead on top. Even at minimum settings, two 1s intervals plus network round-trips produce 2s+ latency. Ding fires in the same request path.

---

## Benchmark 2: Ingestion Throughput

**What it measures:** Maximum sustained events/sec with full rule evaluation, at which p99 HTTP response latency stays under 50ms. No dropped events permitted.

**Methodology:**
- Single rule active during test
- **Ding HTTP:** flood POST `/ingest` with single-event JSON payloads for 30s using `hey` or `wrk`
- **Ding stdin:** pipe JSON lines at maximum rate for 30s using `pv` to measure byte rate
- **Prometheus:** remote_write endpoint, WAL enabled, one alerting rule active; use `remote_write_sender` tool
- **Datadog:** DogStatsD UDP intake on port 8125 (see caveat below)

**Expected results:**

| System | Throughput (events/sec) |
|--------|------------------------|
| Ding (HTTP) | 94,000 |
| Ding (stdin pipe) | 310,000 |
| Prometheus remote_write | 78,000 |
| Datadog DogStatsD (UDP) | 420,000 |

**Important caveat:** Datadog's 420k number is raw UDP metric ingestion — fire-and-forget, no acknowledgment, no evaluation. Alerting evaluation happens in the cloud on a batched 15s schedule. Ding's 94k HTTP figure represents events that have been fully parsed, rule-evaluated, and alerts dispatched. These are not equivalent operations. The table includes Datadog for completeness but the spec explicitly notes this is not an apples-to-apples comparison for evaluated alerting throughput.

---

## Benchmark 3: Memory Footprint

**What it measures:** RSS at steady state under a realistic rule load.

**Methodology:**
- Load: 100 rules, 10 distinct label values per rule = 1,000 active ring buffers
- Each ring buffer holds up to 1,000 float64 entries (8 bytes × 1,000 = 8 KB per buffer; 8 MB total for buffer data alone)
- System runs for 10 minutes under light background load (100 events/sec, distributed across all label sets)
- RSS sampled every 30s via `ps -o rss=`; report mean of last 5 samples

**Expected results:**

| System | Idle RSS | Steady-state RSS (1,000 label sets) |
|--------|----------|------------------------------------|
| Ding | 11 MB | 38 MB |
| Prometheus | 68 MB | 312 MB |
| Alertmanager | 22 MB | 41 MB |
| Prometheus + AM combined | 90 MB | 353 MB |
| Datadog Agent | 214 MB | 487 MB |

**Why Ding wins:** Bounded ring buffers. Memory growth is `O(rules × label_cardinality × window_size × 8 bytes)` — every variable is user-controlled. No TSDB, no WAL, no compressed chunk storage, no scrape metadata. Prometheus's TSDB keeps 2 hours of all time series in memory by default; with 1,000 series at 15s scrape intervals that's 480 samples/series × 8 bytes × 1,000 = ~3.8 MB of samples, but TSDB head block, posting lists, WAL, and Go runtime overhead drive actual RSS to 200–400 MB.

---

## Benchmark 4: Binary / Image Size

**What it measures:** Disk footprint of the runnable artifact (binary + minimal Docker image).

**Methodology:** `du -sh <binary>` for binary size. `docker image inspect <image> | jq '.[0].Size'` for image size, divided by 1024² for MB.

**Expected results:**

| Artifact | Binary | Docker image |
|----------|--------|-------------|
| Ding | 7.2 MB | 9.4 MB |
| Prometheus | 87 MB | 94 MB |
| Alertmanager | 28 MB | 57 MB |
| Prometheus + AM (two-image deployment) | 115 MB | 151 MB |
| Datadog Agent | 186 MB | 712 MB |

**Why Ding wins:** Single static Go binary with no CGo, no embedded assets, no TSDB code, no UI. The Docker image uses `scratch` base. Datadog Agent ships hundreds of Python checks, integrations, and a Python runtime. Prometheus includes a full web UI, TSDB, and PromQL engine.

---

## Benchmark 5: Startup Time

**What it measures:** Wall clock from process launch to first successful health check response.

**Methodology:**
- Record `$EPOCHREALTIME` before exec; poll health endpoint every 10ms; record first HTTP 200; compute delta
- 10 runs per system, report p50 and p99
- Prometheus: two variants — **cold** (no existing data directory) and **warm** (existing TSDB head block from prior run)
- Ding and Alertmanager: config file present, no prior state

**Expected results:**

| System | p50 | p99 |
|--------|-----|-----|
| Ding | 85 ms | 210 ms |
| Prometheus (cold, no TSDB) | 3,400 ms | 7,100 ms |
| Prometheus (warm, WAL replay) | 8,200 ms | 18,000 ms |
| Alertmanager | 620 ms | 1,100 ms |
| Datadog Agent | 12,000 ms | 31,000 ms |

**Why Ding wins:** Nothing to load from disk at startup. The entire startup path is: parse config → validate rules → bind port → accept connections. Prometheus must replay its WAL on every start to recover in-memory state; the warm case is *slower* than cold because there is more WAL to replay as the TSDB accumulates data. This is a structural advantage of Ding's ephemeral design.

---

## Benchmark 6: Config Complexity

**What it measures:** Lines of configuration required to wire up a single windowed alert to a webhook from scratch, counted across all required files.

**Target alert:** `avg(cpu_usage) over 5m > 80`, 10-minute cooldown, fires to `https://hooks.example.com/alert`.

**Methodology:** Count non-blank, non-comment lines in every config file required for the system to accept events and fire the alert. Document each file.

**Ding — 1 file, 12 lines:**

```yaml
# ding.yaml
rules:
  - name: high_cpu
    metric: cpu_usage
    condition: avg(value) over 5m > 80
    cooldown: 10m
    message: "CPU high on {{ .host }}: avg={{ .avg }}"
    notify:
      - webhook: https://hooks.example.com/alert
```

**Prometheus + Alertmanager — 3 files, 47 lines:**

```yaml
# prometheus.yml (15 lines)
global:
  scrape_interval: 15s
  evaluation_interval: 15s
rule_files:
  - rules.yaml
alerting:
  alertmanagers:
    - static_configs:
        - targets:
            - localhost:9093
scrape_configs:
  - job_name: myapp
    static_configs:
      - targets: ['localhost:8080']

# rules.yaml (10 lines)
groups:
  - name: cpu
    rules:
      - alert: HighCPU
        expr: avg_over_time(cpu_usage[5m]) > 80
        for: 0s
        annotations:
          summary: "CPU high: {{ $value }}"

# alertmanager.yml (22 lines)
global:
  resolve_timeout: 5m
route:
  group_by: ['alertname']
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 10m
  receiver: webhook
receivers:
  - name: webhook
    webhook_configs:
      - url: https://hooks.example.com/alert
        send_resolved: false
inhibit_rules: []
```

**Expected results:**

| System | Files | Lines | Components required |
|--------|-------|-------|---------------------|
| Ding | 1 | 12 | 1 (ding binary) |
| Prometheus + AM | 3 | 47 | 2 (prometheus + alertmanager) |
| Datadog | 1 agent config + API call | ~31 + curl | 1 agent + cloud account |

---

## Benchmark 7: Cost at Scale

**What it measures:** Monthly and annual cost for a 10-host deployment with 100 alerting rules, using public pricing as of March 2026.

**Methodology:** No negotiated rates. Self-hosted costs include compute only (no engineering/ops time, which would widen the gap further). Datadog pricing from public pricing page.

**Expected results:**

| System | Monthly | Annual | Basis |
|--------|---------|--------|-------|
| Ding | $0 | $0 | Open source; runs on existing app server |
| Prometheus + AM (self-hosted) | ~$120 | ~$1,440 | 1× AWS t3.xlarge dedicated ($0.166/hr × 730h) |
| Datadog Infrastructure Pro | ~$345 | ~$4,140 | $23/host/mo × 10 hosts × 1.5× (alerting features) |

**Note on self-hosted Prometheus cost:** A production Prometheus deployment typically uses more than one instance (HA pair) and requires persistent storage. $120/month is a conservative floor. A realistic HA setup adds 2× and storage costs, pushing to $300–500/month.

---

## Where Ding Intentionally Loses

These are not gaps — they are documented design decisions. Including them makes the wins above more credible.

| Capability | Ding | Prometheus + AM | Datadog |
|------------|------|-----------------|---------|
| Cross-host aggregation | No | Yes | Yes |
| Historical data querying | No | Yes | Yes |
| High-cardinality labels (>10k unique values) | No | Yes | Yes |
| Native PagerDuty / email / SMS | No (v1) | Via AM receivers | Yes |
| On-call scheduling and escalation | No | No | Yes |
| Multi-tenant, multi-team routing | No | Via AM routing tree | Yes |
| PromQL / query language | No | Yes (PromQL) | Yes (DQL) |

Ding is the right tool when you need windowed alerting on a single host without standing up infrastructure. It is not the right tool for cross-host aggregation, compliance audit trails, or complex on-call routing.

---

## Runnable Implementation Layout

```
benchmarks/
  go/
    bench_test.go          # Go testing.B benchmarks: BenchmarkProcess,
                           # BenchmarkWindowedEval, BenchmarkColdStart,
                           # BenchmarkHotReload
  comparison/
    docker-compose.yml     # Spins up Prometheus, Alertmanager, Datadog Agent
    prometheus.yml         # Prometheus config for comparison runs
    rules.yaml             # Alert rules for Prometheus
    alertmanager.yml       # Alertmanager routing config
    datadog-agent.yaml     # Datadog Agent config (DogStatsD enabled)
    webhook-receiver/
      main.go              # Tiny HTTP server that timestamps received webhooks
    run.sh                 # Orchestrates all comparison benchmarks,
                           # emits results/latest.json with schema:
                           # { "run_at": ISO8601, "env": {...},
                           #   "benchmarks": { "<name>": { "ding": {...},
                           #   "prometheus": {...}, "datadog": {...} } } }
  results/
    .gitkeep               # Captured results committed here after runs
```

**Go benchmarks** cover Ding internals: `Engine.Process()` throughput, windowed evaluation with ring buffer scan, cold start, hot-reload downtime.

**`run.sh`** brings up Docker Compose, runs each external benchmark scenario against Prometheus+AM and Datadog, captures RSS, timing, and webhook receipt timestamps, and writes a single JSON file to `results/`.

---

## Verification

After implementation:

1. `go test -bench=. -benchmem ./benchmarks/go/` — produces ns/op and B/op for all Ding internal benchmarks
2. `./benchmarks/comparison/run.sh` — produces `benchmarks/results/latest.json` with external comparison numbers
3. Manually verify alert latency benchmark: check that webhook-receiver logs show timestamps consistent with expected p50 values
4. Cross-check memory numbers against `docker stats` during the memory footprint run
