# DING — Product Positioning & Marketing Document

**Date:** 2026-03-22
**Status:** Draft

---

## The Tagline

> *Don't store it. Stream it. DING it.*

---

## The One-Sentence Pitch

DING is the alerting tool for developers who would rather ship than configure — a single binary that watches a stream of metrics and fires alerts when conditions are met, with zero infrastructure, zero storage, and zero operational overhead.

---

## The Problem We're Solving

Every developer has written this code:

```python
if error_count > 100:
    requests.post(SLACK_WEBHOOK_URL, json={"text": "too many errors"})
```

It works until it doesn't. It has no cooldown, so it spams Slack every second. It has no windowing, so a spike that lasted two seconds looks the same as one that lasted two hours. It has no config file, so the threshold is hardcoded in a script somewhere, committed to a branch that's six months out of date. And when it breaks, nobody knows where it lives.

The alternative — Prometheus, Grafana, Alertmanager — is three separate systems, a persistent volume, a scrape target architecture, and a week of configuration before you see your first alert. For a startup, an edge device, a CI pipeline, or an internal tool, that's not a trade-off. It's a non-starter.

The gap between "a fragile script" and "a full observability platform" is enormous. DING lives in that gap.

---

## What DING Is

DING is a **stream-based alerting daemon**. It reads a stream of metrics — from stdin, from an HTTP endpoint, or both simultaneously — evaluates user-defined rules, and fires alerts when conditions are met. It runs as a single static binary. It requires no database, no message queue, no sidecar, and no cloud account. Drop it on any server and it works.

### The Core Loop

```
[your app]  ──stdin──►  ┌─────────────┐
                         │    DING     │  ──► Slack webhook
[curl/agent] ──HTTP──►  │             │  ──► PagerDuty proxy
                         │  1. Parse   │  ──► stdout (pipeable)
                         │  2. Match   │
                         │  3. Evaluate│
                         │  4. Cooldown│
                         │  5. Alert   │
                         └─────────────┘
                                ▲
                           ding.yaml
                     (lives in your repo)
```

### What Makes It Different

Most developer tooling sits at one of two extremes:

| **Unix primitives** | **Observability platforms** |
|---|---|
| `grep`, `awk`, `watch` | Prometheus, Grafana, Alertmanager, Datadog |
| Composable, no infrastructure | Full-featured, full commitment |
| Zero setup time | Days to weeks to configure |
| No alerting semantics | Everything — and a team to manage it |

DING occupies the gap: **a composable alerting primitive with real rule semantics**.

`grep` can match a pattern but cannot tell you "this matched more than 50 times in the last minute." Prometheus can do windowed aggregation but requires a scrape architecture, persistent storage, and a separately-managed Alertmanager. Shell scripts can count things but cannot do per-label cooldowns without writing a state machine from scratch.

DING's specific combination does not exist elsewhere:

- **Windowed aggregations without storage** — ring buffers in memory, evicted by wall clock. `avg(value) over 5m > 80` works with no database.
- **Per-label-set cooldowns** — not just "this rule fired, wait 1 minute," but "this rule fired for `host=web-01`, wait 1 minute — `host=web-02` is still live."
- **Unix composability** — `your-app | ding serve` works identically to a `POST /ingest`. Output is JSON lines that pipe into anything downstream.
- **Config lives in your repo** — `ding.yaml` is committed alongside the code that emits the metrics. Alerting is a development-time artifact, not an ops-time artifact.

That last point is the philosophical core. With Prometheus, your alert rules are managed by a separate team in a separate system. With DING, the developer who writes `{"metric":"payment_timeout","value":1}` also writes and ships the rule that alerts on it. The feedback loop collapses.

---

## Real-World Scenarios: Where DING Wins

### 1. Single-Server Applications with Tight SLAs

**The situation:** A payment processor on one VM wants millisecond-level alerting when error rates spike. Installing Prometheus and Alertmanager would take a week and add two new failure modes.

**With DING:**
```bash
tail -f /var/log/payments.json | ding serve --config payments-alerts.yaml
```

Alert fires in milliseconds, on the same machine, with no external dependencies. Cooldown prevents Slack from being spammed during an outage. The rule is versioned in the same repository as the application.

---

### 2. CI/CD Pipelines and Build Systems

**The situation:** A build pipeline emits metrics — test durations, flaky test counts, artifact sizes, coverage percentages. The team wants alerts when regressions appear, before the build finishes.

**With DING:**
```bash
run-tests --json | ding serve --config pipeline-alerts.yaml
```

DING starts when the pipeline starts, alerts when thresholds are crossed, and exits when the pipeline exits. No persistent process, no cleanup, no residual state.

---

### 3. Edge and Embedded Deployments

**The situation:** Industrial machinery, retail point-of-sale systems, agricultural sensors, Raspberry Pi clusters. These environments have constrained resources, intermittent connectivity, and no path to a centralized monitoring stack.

**With DING:** A 5MB static binary runs on anything Go compiles for — linux/arm64, linux/amd64, darwin, windows — with no runtime dependencies. It can alert locally (stdout to a local log), or fire webhooks when connectivity is available. The alert rules ship with the firmware.

---

### 4. Local Development and Debugging

**The situation:** A developer wants to reproduce a production alerting condition locally. The production monitoring stack is not available in their local environment.

**With DING:**
```bash
cat test-fixtures/high-error-scenario.jsonl | ding serve --config prod-alerts.yaml
```

Exact production rules, exact production thresholds, running locally in seconds. No staging environment required, no infrastructure access required.

---

### 5. Scripted and Batch Workloads

**The situation:** A nightly ETL job processes millions of rows and emits row counts, error rates, and processing time metrics. The team wants alerts if the job runs long, drops rows, or hits unexpected error rates.

**With DING:**
```bash
python etl.py | ding serve --config etl-alerts.yaml
```

DING starts with the script and exits with the script. No long-running daemon to manage. The alert config is part of the ETL project, not a separate ops system.

---

### 6. Early-Stage Startups

**The situation:** A 3-person team is shipping fast. They know they need alerting. They do not have time to learn Prometheus query language, set up persistent storage, configure Alertmanager routing, or manage three new infrastructure components.

**With DING:** `cp ding.yaml.example ding.yaml`, edit two lines, `ding serve`. Alerting is live in under five minutes. When the team grows and outgrows DING, they replace it — but it bought them six to twelve months of production visibility with an afternoon of work.

---

## Where DING Is Not the Right Tool

Honesty is a feature of good positioning. DING is not trying to replace the observability stack for mature engineering organizations. The following scenarios require a different tool:

**Monitoring a fleet of servers** — DING has no cross-host aggregation. "Alert if more than 10 of my 500 servers are in a degraded state" requires a centralized time-series database. Each DING instance knows only what flows through it.

**Historical analysis and replay** — DING has no storage. You cannot query past data. If you need to ask "would this alert have fired last Tuesday?", you need a TSDB.

**Complex on-call routing** — Escalation policies, on-call schedules, deduplication, and acknowledgment workflows are out of scope. DING fires webhooks; what happens after the webhook is someone else's problem.

**High-cardinality label environments** — If your metrics are tagged with `request_id`, `trace_id`, or `user_id`, DING will create one ring buffer per unique label combination per rule. At high cardinality, this grows unboundedly. DING is designed for infrastructure-level label cardinality (host, region, service), not request-level.

**Compliance and audit** — Regulatory requirements for proof that an alert did or did not fire require persistent storage. DING's outputs are ephemeral.

---

## The Target User

**Primary:** A backend developer at a company of 5–100 engineers who is responsible for the reliability of a service they wrote. They are not a dedicated SRE. They don't manage Prometheus. They have a Slack webhook URL and a `ding.yaml` file.

**Secondary:** A platform or infrastructure engineer at a larger company who needs to add alerting to an edge deployment, a batch job, or a CI system where the main observability stack is unavailable or inappropriate.

**Anti-target:** A large enterprise with a dedicated SRE team that already runs Datadog or a self-managed Prometheus stack. DING solves a problem they don't have.

---

## Competitive Landscape

| Tool | What it does | Why DING is different |
|------|-------------|----------------------|
| **Prometheus + Alertmanager** | Full metrics collection, storage, and alerting | Requires persistent storage, scrape architecture, 3+ components, dedicated ops. DING requires none of these. |
| **Datadog / New Relic / Grafana Cloud** | SaaS observability platforms | Sends your data to a third party, costs money at scale, requires an agent. DING runs entirely on your infrastructure. |
| **Grafana OSS** | Visualization and alerting over existing data sources | Requires an existing data source (Prometheus, InfluxDB, etc.). DING IS the pipeline. |
| **Custom scripts** | Grep + curl + cron | No windowing, no cooldowns, no composability, brittle. DING replaces this with something maintainable. |
| **Fluentd / Vector** | Log routing and transformation pipelines | Not alerting-focused. Rules are complex to write. No notion of thresholds or cooldowns. |
| **Alertmanager (standalone)** | Alert routing and deduplication | Receives alerts from Prometheus, does not evaluate conditions. Not useful without a metrics store. |

---

## Distribution Model

DING is open source (MIT). The distribution strategy prioritizes developer trust and adoption speed:

- **GitHub Releases** — Pre-built binaries for all platforms. `curl`, extract, run.
- **Homebrew** — `brew install zuchka/tap/ding`. The standard developer install path on macOS.
- **Docker** — `docker run -v ./ding.yaml:/etc/ding/ding.yaml ghcr.io/zuchka/ding`. A 5MB scratch-based image.
- **Install script** — `curl -sf https://start.ding.ing | sh`. The one-liner that works everywhere.

No account required. No telemetry. No license key. No phone-home. The binary works immediately, on your hardware, without asking permission.

---

## Why Now

The developer tooling market has bifurcated. On one end: primitive Unix tools that compose but have no semantics. On the other: full-featured SaaS platforms with per-seat pricing and vendor lock-in. The middle — maintainable, composable, semantically rich tooling that a single developer can operate — has been underserved.

At the same time, the explosion of edge deployments (IoT, embedded Linux, wasm), the proliferation of internal tooling teams, and the growing emphasis on developer ownership of reliability ("you build it, you run it") have created a large and growing population of developers who need alerting but cannot or will not operate a full observability stack.

DING is the tool that fills that gap — the missing Unix utility for alerting.

---

*DING is open source under the MIT license. Contributions welcome at github.com/zuchka/ding.*
