#!/usr/bin/env bash
# benchmarks/comparison/scripts/bench-memory.sh
#
# Measures RSS for each system with 100 rules × 10 label values active.
# Requires: docker compose services running (prometheus, alertmanager, datadog-agent)
# and Ding running with a 100-rule config (generated below).
#
# Outputs JSON: {"ding_mb":38,"prometheus_mb":312,"alertmanager_mb":41,"datadog_mb":487}

set -euo pipefail

DURATION="${DURATION:-600}"  # 10 minutes
SAMPLE_INTERVAL=30

# Generate a 100-rule Ding config
gen_ding_config() {
  local path="$1"
  echo "rules:" > "$path"
  for i in $(seq 0 99); do
    cat >> "$path" << EOF
  - name: rule_$(printf '%03d' $i)
    metric: cpu_usage_$i
    condition: avg(value) over 5m > 80
    cooldown: 1m
    notify:
      - webhook: http://localhost:9999/
EOF
  done
}

# Generate events for 10 label values per rule
flood_ding() {
  local base_url="$1"
  for host in $(seq 1 10 | xargs printf 'web-%02d\n'); do
    for metric in $(seq 0 99 | xargs printf 'cpu_usage_%d\n'); do
      curl -sf -X POST "$base_url/ingest" \
        -H "Content-Type: application/json" \
        -d "{\"metric\":\"$metric\",\"value\":50,\"host\":\"$host\"}" > /dev/null
    done
  done
}

# RSS sampler: reads /proc/$PID/status or uses `ps` on macOS
get_rss_mb() {
  local pid="$1"
  # macOS: ps reports RSS in KB
  ps -o rss= -p "$pid" | awk '{printf "%d", $1/1024}'
}

# Start Ding with 100-rule config
DING_CONFIG=$(mktemp /tmp/bench-ding-XXXXXX.yaml)
gen_ding_config "$DING_CONFIG"
./ding serve --config "$DING_CONFIG" &
DING_PID=$!
sleep 1

# Flood with 1000 label-set combinations to populate ring buffers
flood_ding "http://localhost:8080"

# Sample RSS every 30s for DURATION seconds; keep last 5 samples for the mean.
# This matches the spec methodology: "RSS sampled every 30s, mean of last 5 samples."
SAMPLE_COUNT=0
declare -a DING_SAMPLES PROM_SAMPLES AM_SAMPLES DD_SAMPLES

parse_mib() { echo "$1" | awk -F'[iB /]' '{printf "%d", $1}'; }

t_end=$(( $(date +%s) + DURATION ))
while [ "$(date +%s)" -lt "$t_end" ]; do
  # Sustain light traffic during sampling window (100 events total per interval)
  for _s in $(seq 1 100); do
    curl -sf -X POST http://localhost:8080/ingest \
      -H "Content-Type: application/json" \
      -d '{"metric":"cpu_usage_0","value":50,"host":"web-01"}' > /dev/null
  done

  PROM_STATS=$(docker stats --no-stream --format '{{.MemUsage}}' \
    "$(docker compose -f benchmarks/comparison/docker-compose.yml ps -q prometheus)" 2>/dev/null || echo "0MiB")
  AM_STATS=$(docker stats --no-stream --format '{{.MemUsage}}' \
    "$(docker compose -f benchmarks/comparison/docker-compose.yml ps -q alertmanager)" 2>/dev/null || echo "0MiB")
  DD_STATS=$(docker stats --no-stream --format '{{.MemUsage}}' \
    "$(docker compose -f benchmarks/comparison/docker-compose.yml ps -q datadog-agent)" 2>/dev/null || echo "0MiB")

  DING_SAMPLES+=( "$(get_rss_mb "$DING_PID")" )
  PROM_SAMPLES+=( "$(parse_mib "$PROM_STATS")" )
  AM_SAMPLES+=( "$(parse_mib "$AM_STATS")" )
  DD_SAMPLES+=( "$(parse_mib "$DD_STATS")" )
  SAMPLE_COUNT=$(( SAMPLE_COUNT + 1 ))

  sleep "$SAMPLE_INTERVAL"
done

# Mean of last 5 samples (or all if fewer than 5)
mean_last5() {
  local arr=("$@")
  local n=${#arr[@]}
  local start=$(( n > 5 ? n - 5 : 0 ))
  local sum=0
  for i in $(seq "$start" $(( n - 1 ))); do
    sum=$(( sum + arr[i] ))
  done
  local count=$(( n - start ))
  echo $(( sum / count ))
}

ding_mb=$(mean_last5 "${DING_SAMPLES[@]}")
prom_mb=$(mean_last5 "${PROM_SAMPLES[@]}")
am_mb=$(mean_last5 "${AM_SAMPLES[@]}")
dd_mb=$(mean_last5 "${DD_SAMPLES[@]}")

kill "$DING_PID" 2>/dev/null || true
rm -f "$DING_CONFIG"

jq -n \
  --argjson ding "$ding_mb" \
  --argjson prom "$prom_mb" \
  --argjson am "$am_mb" \
  --argjson dd "$dd_mb" \
  '{"ding_mb":$ding,"prometheus_mb":$prom,"alertmanager_mb":$am,"datadog_mb":$dd}'
