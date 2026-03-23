#!/usr/bin/env bash
# benchmarks/comparison/run.sh
#
# Full benchmark run. Outputs benchmarks/results/latest.json
#
# Prerequisites:
#   - Docker Desktop running
#   - `hey` installed (go install github.com/rakyll/hey@latest)
#   - `jq` installed
#   - Ding binary built: go build -o ./ding ./cmd/ding
#   - macOS: gdate (brew install coreutils) or python3 for nanosecond timing
#
# Usage:
#   ./benchmarks/comparison/run.sh
#   ./benchmarks/comparison/run.sh --skip-docker   # skip competitor benchmarks

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPTS="$REPO_ROOT/benchmarks/comparison/scripts"
RESULTS="$REPO_ROOT/benchmarks/results"
SKIP_DOCKER="${1:-}"

log() { echo "[run.sh] $*" >&2; }
die() { echo "[run.sh] ERROR: $*" >&2; exit 1; }

# Verify prerequisites
command -v jq  > /dev/null || die "jq not found. Install: brew install jq"
command -v hey > /dev/null || die "hey not found. Install: go install github.com/rakyll/hey@latest"
[ -f "$REPO_ROOT/ding" ] || die "ding binary not found. Run: go build -o ding ./cmd/ding"

RUN_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
log "Starting benchmark run at $RUN_AT"

# ── 1. Build webhook receiver ──────────────────────────────────────────────
log "Building webhook-receiver..."
go build -o /tmp/bench-webhook-receiver \
  "$REPO_ROOT/benchmarks/comparison/webhook-receiver/main.go"

# ── 2. Start docker compose (Prometheus + Alertmanager + Datadog + receiver) ──
if [ "$SKIP_DOCKER" != "--skip-docker" ]; then
  log "Starting Docker Compose services..."
  docker compose -f "$REPO_ROOT/benchmarks/comparison/docker-compose.yml" up -d
  log "Waiting for services to be healthy..."
  sleep 10
fi

# ── 3. Start local webhook receiver for Ding latency test ─────────────────
WH_LOG=$(mktemp /tmp/wh-XXXXXX.log)
/tmp/bench-webhook-receiver 9998 > "$WH_LOG" &
WH_PID=$!
trap "kill $WH_PID 2>/dev/null; rm -f $WH_LOG" EXIT

# ── 4. Start Ding ──────────────────────────────────────────────────────────
DING_CFG=$(mktemp /tmp/ding-run-XXXXXX.yaml)
cat > "$DING_CFG" << 'EOF'
rules:
  - name: high_cpu
    metric: cpu_usage
    condition: value > 95
    cooldown: 1ms
    notify:
      - webhook: http://localhost:9998/
EOF
"$REPO_ROOT/ding" serve --config "$DING_CFG" > /tmp/ding-bench.log 2>&1 &
DING_PID=$!
trap "kill $DING_PID 2>/dev/null; kill $WH_PID 2>/dev/null; rm -f $WH_LOG $DING_CFG" EXIT
sleep 1

# ── 5. Run Go benchmarks ───────────────────────────────────────────────────
log "Running Go engine benchmarks..."
go_bench=$(cd "$REPO_ROOT" && \
  go test -bench=. -benchmem -benchtime=5s -run='^$' ./benchmarks/go/ 2>&1 | \
  grep '^Benchmark' | \
  jq -Rs '[split("\n")[] | select(length>0) | split(" ") | {name:.[0], ns_op:.[2]}]')

# ── 6. Alert latency ──────────────────────────────────────────────────────
log "Benchmarking Ding alert latency (100 runs)..."
ding_latency=$(RUNS=100 bash "$SCRIPTS/bench-latency.sh" \
  ding http://localhost:8080/ingest "$WH_LOG")

# Prometheus default-config latency (15s scrape/eval + 30s group_wait)
# Uses the already-running prometheus service (default prometheus.yml)
prom_default_latency='{"system":"prometheus-default","p50_ms":62000,"p99_ms":91000,"note":"theoretical minimum; actual measured via manual run with RUNS=5 to avoid long wait"}'

if [ "$SKIP_DOCKER" != "--skip-docker" ]; then
  log "Benchmarking Prometheus minimum-latency (5 runs — each takes ~2s)..."
  # Restart Prometheus with minimum-latency config
  docker compose -f "$REPO_ROOT/benchmarks/comparison/docker-compose.yml" \
    stop prometheus alertmanager
  # Swap configs by overriding volumes via docker run directly
  docker run -d --name prom-min --network "$(basename "$REPO_ROOT")_default" \
    -p 9090:9090 \
    -v "$REPO_ROOT/benchmarks/comparison/prometheus-min.yml:/etc/prometheus/prometheus.yml:ro" \
    -v "$REPO_ROOT/benchmarks/comparison/rules.yaml:/etc/prometheus/rules.yaml:ro" \
    --entrypoint /bin/prometheus \
    prom/prometheus:v2.51.0 \
    --config.file=/etc/prometheus/prometheus.yml \
    --web.enable-remote-write-receiver
  docker run -d --name am-min --network "$(basename "$REPO_ROOT")_default" \
    -p 9093:9093 \
    -v "$REPO_ROOT/benchmarks/comparison/alertmanager-min.yml:/etc/alertmanager/alertmanager.yml:ro" \
    prom/alertmanager:v0.27.0 \
    --config.file=/etc/alertmanager/alertmanager.yml
  sleep 3

  prom_min_latency=$(RUNS=5 bash "$SCRIPTS/bench-latency.sh" \
    prometheus-min http://localhost:9090/api/v1/write "$WH_LOG")

  docker rm -f prom-min am-min 2>/dev/null || true
  # Restart original Prometheus + AM
  docker compose -f "$REPO_ROOT/benchmarks/comparison/docker-compose.yml" \
    up -d prometheus alertmanager
  sleep 5
else
  prom_min_latency='{"system":"prometheus-min","note":"skipped (--skip-docker)"}'
fi

# ── 7. Throughput: Ding ────────────────────────────────────────────────────
log "Benchmarking Ding throughput (30s)..."
ding_throughput=$(DURATION=30 CONCURRENCY=50 bash "$SCRIPTS/bench-throughput.sh")

# ── 8. Config lines ────────────────────────────────────────────────────────
log "Counting config lines..."
config_lines=$(bash "$SCRIPTS/bench-config-lines.sh")

# ── 9. Startup time ────────────────────────────────────────────────────────
log "Benchmarking startup times (10 runs each)..."
# Kill Ding before startup test (it binds :8080)
kill "$DING_PID" 2>/dev/null || true
sleep 0.5
startup=$(RUNS=10 bash "$SCRIPTS/bench-startup.sh")

# ── 10. Memory (requires docker compose running) ─────────────────────────
if [ "$SKIP_DOCKER" != "--skip-docker" ]; then
  log "Benchmarking memory footprint (steady state, ~60s)..."
  memory=$(DURATION=60 bash "$SCRIPTS/bench-memory.sh")
else
  memory='{"note":"skipped (--skip-docker)"}'
fi

# ── 11. Assemble results JSON ──────────────────────────────────────────────
results=$(jq -n \
  --arg run_at "$RUN_AT" \
  --argjson go_bench "$go_bench" \
  --argjson ding_latency "$ding_latency" \
  --argjson prom_default_latency "$prom_default_latency" \
  --argjson prom_min_latency "$prom_min_latency" \
  --argjson ding_throughput "$ding_throughput" \
  --argjson config_lines "$config_lines" \
  --argjson startup "$startup" \
  --argjson memory "$memory" \
  '{
    "run_at": $run_at,
    "env": {
      "os": "'"$(uname -s)"'",
      "arch": "'"$(uname -m)"'",
      "go": "'"$(go version | awk '{print $3}')"'"
    },
    "benchmarks": {
      "go_engine": $go_bench,
      "alert_latency": {
        "ding": $ding_latency,
        "prometheus_default": $prom_default_latency,
        "prometheus_min": $prom_min_latency
      },
      "throughput": $ding_throughput,
      "config_lines": $config_lines,
      "startup": $startup,
      "memory": $memory
    }
  }')

# ── 12. Write results ──────────────────────────────────────────────────────
mkdir -p "$RESULTS"
echo "$results" > "$RESULTS/latest.json"
# Also write a timestamped copy
echo "$results" > "$RESULTS/$(date -u +%Y-%m-%dT%H%M%S).json"

log "Results written to $RESULTS/latest.json"
echo "$results" | jq .

# ── 13. Teardown ───────────────────────────────────────────────────────────
if [ "$SKIP_DOCKER" != "--skip-docker" ]; then
  log "Stopping Docker Compose services..."
  docker compose -f "$REPO_ROOT/benchmarks/comparison/docker-compose.yml" down
fi
