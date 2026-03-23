#!/usr/bin/env bash
# benchmarks/comparison/scripts/bench-latency.sh
#
# Usage: bench-latency.sh <system> <ingest_url> <webhook_log>
#   system:      ding | prometheus-default | prometheus-min | datadog
#   ingest_url:  URL to POST event to (for ding/prometheus) or DogStatsD address (for datadog)
#   webhook_log: path to file where webhook-receiver writes timestamps (one per line)
#
# For the two Prometheus variants, run.sh calls this script twice:
#   - prometheus-default: docker-compose with prometheus.yml (15s scrape/eval)
#   - prometheus-min: docker-compose with prometheus-min.yml (1s scrape/eval, group_wait 0s)
#
# Outputs JSON: {"system":"ding","p50_ms":2,"p99_ms":8,"samples":[...]}

set -euo pipefail

# ns_now: portable nanosecond timestamp (macOS BSD date lacks %N)
ns_now() {
  if command -v gdate > /dev/null 2>&1; then
    gdate +%s%N
  else
    python3 -c "import time; print(int(time.time()*1e9))"
  fi
}

SYSTEM="${1:?system required}"
INGEST_URL="${2:?ingest_url required}"
WEBHOOK_LOG="${3:?webhook_log required}"
RUNS="${RUNS:-100}"

samples=()

for i in $(seq 1 "$RUNS"); do
  # Clear webhook log so we get a clean receipt
  > "$WEBHOOK_LOG"

  # Record send time (nanoseconds)
  t_send=$(ns_now)

  case "$SYSTEM" in
    ding)
      curl -sf -X POST "$INGEST_URL" \
        -H "Content-Type: application/json" \
        -d '{"metric":"cpu_usage","value":97,"host":"bench-01"}' > /dev/null
      ;;
    prometheus-default|prometheus-min)
      # Push a sample via remote_write receiver (--web.enable-remote-write-receiver
      # is set in docker-compose.yml). promtool push metrics uses text exposition
      # format over HTTP — no protobuf needed.
      # Install: go install github.com/prometheus/prometheus/cmd/promtool@v2.51.0
      promtool push metrics "$INGEST_URL" <<'EOF'
# HELP cpu_usage CPU usage percent
# TYPE cpu_usage gauge
cpu_usage{host="bench-01"} 97
EOF
      ;;
    datadog)
      # DogStatsD gauge: metric:value|type
      echo "cpu_usage:97|g|#host:bench-01" | nc -u -w1 127.0.0.1 8125
      ;;
  esac

  # Wait for webhook receipt — poll every 10ms up to 15 seconds.
  # Timeout is tracked in wall-clock nanoseconds so it works for both
  # Ding (<10ms) and Prometheus (up to 120s with default settings).
  t_poll_start=$(ns_now)
  timeout_ns=$(( 15 * 1000000000 ))
  while [ ! -s "$WEBHOOK_LOG" ]; do
    sleep 0.01
    t_now=$(ns_now)
    if (( t_now - t_poll_start >= timeout_ns )); then
      break
    fi
  done

  if [ ! -s "$WEBHOOK_LOG" ]; then
    echo "WARN: no webhook received within $(( timeout_ns / 1000000000 ))s on run $i" >&2
    continue
  fi

  t_recv=$(head -1 "$WEBHOOK_LOG")
  latency_ms=$(( (t_recv - t_send) / 1000000 ))
  samples+=("$latency_ms")
done

# Compute p50 and p99
if (( ${#samples[@]} == 0 )); then
  echo "ERROR: bench-latency.sh($SYSTEM): no successful samples — webhook never received" >&2
  jq -n --arg system "$SYSTEM" '{"system":$system,"p50_ms":null,"p99_ms":null,"samples":[],"error":"no webhook receipts"}'
  exit 1
fi

sorted=($(printf '%s\n' "${samples[@]}" | sort -n))
count=${#sorted[@]}
p50_idx=$(( count / 2 ))
p99_idx=$(( count * 99 / 100 ))

p50=${sorted[$p50_idx]}
p99=${sorted[$p99_idx]}

# Emit JSON
jq -n \
  --arg system "$SYSTEM" \
  --argjson p50 "$p50" \
  --argjson p99 "$p99" \
  --argjson samples "$(printf '%s\n' "${samples[@]}" | jq -s .)" \
  '{"system":$system,"p50_ms":$p50,"p99_ms":$p99,"samples":$samples}'
