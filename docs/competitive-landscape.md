# DING — Competitive Landscape

**Axes:**
- **X-axis:** Operational overhead — from zero (single binary, no dependencies) to heavy (multiple services, persistent storage, dedicated ops)
- **Y-axis:** Alerting capability — from basic (pattern matching, simple scripts) to advanced (windowed aggregations, per-label cooldowns, routing)

---

```
▲
│
│  ADVANCED ALERTING
│  (windowed rules, cooldowns,
│   label-set tracking, routing)
│
│  ┌─────────────────────────┬─────────────────────────────────────────┐
│  │                         │                                         │
│  │   ★ DING                │   Prometheus + Alertmanager             │
│  │                         │                                         │
│  │   The only tool with    │   Advanced rules and routing, but       │
│  │   real alerting         │   requires a scrape architecture,       │
│  │   semantics and zero    │   persistent storage, and a             │
│  │   infrastructure.       │   separately-managed Alertmanager.      │
│  │                         │   Minimum viable setup: 3 services,     │
│  │   Single binary.        │   a volume, and an afternoon.           │
│  │   Runs in seconds.      │                                         │
│  │   Config in your repo.  │   ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─  │
│  │                         │                                         │
│  │                         │   Datadog / New Relic / Grafana Cloud   │
│  │                         │                                         │
│  │                         │   Managed SaaS equivalents. Powerful,   │
│  │                         │   but your data leaves your             │
│  │                         │   infrastructure, per-seat pricing       │
│  │                         │   scales uncomfortably, and there's     │
│  │                         │   an agent to install and maintain.     │
│  │                         │                                         │
│  ├─────────────────────────┼─────────────────────────────────────────┤
│  │                         │                                         │
│  │   grep / awk / cron     │   Vector / Fluentd / Logstash           │
│  │   + curl to Slack       │                                         │
│  │                         │   Pipeline and routing tools. They      │
│  │   Where everyone        │   transform and forward data but have   │
│  │   starts. No windowing, │   no notion of thresholds, cooldowns,   │
│  │   no cooldowns, no      │   or windowed aggregations. Not         │
│  │   config file. Breaks   │   alerting tools — they feed alerting   │
│  │   silently. Spams       │   tools.                                │
│  │   Slack. Hardcoded      │                                         │
│  │   thresholds buried     │   ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─  │
│  │   in a script.          │                                         │
│  │                         │   Grafana OSS                           │
│  │                         │                                         │
│  │                         │   Visualization layer only — requires   │
│  │                         │   an existing data source (Prometheus,  │
│  │                         │   InfluxDB, etc.) to function. Does     │
│  │                         │   not ingest or evaluate metrics.       │
│  │                         │                                         │
│  └─────────────────────────┴─────────────────────────────────────────┘
│
│  BASIC / AD-HOC
│  (pattern matching, one-off scripts,
│   no persistent rules)
│
└──────────────────────────────────────────────────────────────────────────►
   ZERO INFRASTRUCTURE                                    HEAVY INFRASTRUCTURE
   (single binary, no deps)                    (storage, agents, multiple services)
```

---

## Reading the Diagram

**Top-left — DING's unique position.** Advanced alerting semantics with zero operational overhead. No other tool occupies this cell. The tools above DING on the Y-axis (Prometheus, Datadog) all require significant infrastructure investment. The tools beside DING on the X-axis (grep/scripts) lack windowed aggregation, per-label cooldowns, and maintainable config.

**Top-right — The incumbent platforms.** Prometheus + Alertmanager is the gold standard for self-hosted alerting at scale. Datadog and New Relic are its SaaS equivalents. These are the right choice for mature engineering organizations monitoring distributed systems. They are the wrong choice for a single server, a CI job, an edge device, or a team that needs alerting in under five minutes.

**Bottom-left — Where developers start.** `grep | curl` works until it doesn't. No windowing, no cooldowns, hardcoded thresholds, silent failures, and Slack spam during outages. DING is the upgrade path from here that doesn't require learning a new infrastructure stack.

**Bottom-right — Pipeline tools, not alerting tools.** Vector and Fluentd route and transform data. Grafana visualizes it. Neither evaluates conditions or fires alerts without an alerting engine behind them. They are the plumbing; DING is the smoke alarm.

---

## The Gap DING Fills

```
  Alerting                                              Alerting
  sophistication:  LOW ◄──────────────────────► HIGH   sophistication:
  no windowing,                                         windowed rules,
  no cooldowns              ★ DING                      cooldowns,
                                                        label-set tracking
  Infrastructure: ─────────────────────────────────── Infrastructure:
  ZERO                                                  HEAVY
  (single binary)                                       (3+ services,
                                                         storage, agents)
```

Every other tool forces a trade-off: sophistication costs infrastructure. DING breaks that trade-off for the specific case where you are watching a single stream of data on a single machine.
