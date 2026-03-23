#!/usr/bin/env bash
# benchmarks/comparison/scripts/bench-startup.sh
#
# Measures wall-clock time from process launch to first healthy response.
# Reports p50 and p99 over 10 runs for each system.
# Outputs JSON: {"ding":{"p50_ms":85,"p99_ms":210},"prometheus":{"p50_ms":3400,...},...}
#
# macOS note: requires gdate (brew install coreutils) or python3 for nanosecond timing.

set -euo pipefail

RUNS="${RUNS:-10}"

# ns_now: portable nanosecond timestamp (macOS BSD date lacks %N)
ns_now() {
  if command -v gdate > /dev/null 2>&1; then
    gdate +%s%N
  else
    python3 -c "import time; print(int(time.time()*1e9))"
  fi
}

time_to_healthy() {
  local name="$1"
  local start_cmd="$2"
  local health_url="$3"
  local stop_cmd="$4"
  # Extract port from health URL (e.g. http://localhost:8083/health → 8083)
  local port
  port=$(echo "$health_url" | grep -oE ':[0-9]+' | head -1 | tr -d ':')

  local times=()
  for _i in $(seq 1 "$RUNS"); do
    local t_start
    t_start=$(ns_now)
    eval "$start_cmd" > /dev/null 2>&1 &
    local pid=$!

    # Poll health endpoint
    local ready=false
    for _j in $(seq 1 300); do  # 300 × 10ms = 30s max (covers Prometheus cold start)
      if curl -sf "$health_url" > /dev/null 2>&1; then
        ready=true
        break
      fi
      sleep 0.01
    done

    local t_ready
    t_ready=$(ns_now)
    # Graceful stop, then force-kill pid and any orphaned ding/docker processes
    eval "$stop_cmd $pid" 2>/dev/null || true
    sleep 0.1
    kill -9 "$pid" 2>/dev/null || true
    pkill -9 -f "ding serve" 2>/dev/null || true
    docker rm -f $(docker ps -q --filter "publish=$port") > /dev/null 2>&1 || true
    # Wait for the port to be released before the next run
    local attempts=0
    while nc -z 127.0.0.1 "$port" 2>/dev/null && (( attempts < 100 )); do
      sleep 0.1
      attempts=$(( attempts + 1 ))
    done

    if $ready; then
      local ms=$(( (t_ready - t_start) / 1000000 ))
      times+=("$ms")
    fi
  done

  if (( ${#times[@]} == 0 )); then
    echo "ERROR: time_to_healthy($name): no successful runs — health check never fired" >&2
    echo "{\"name\":\"$name\",\"p50_ms\":null,\"p99_ms\":null,\"error\":\"no successful runs\"}"
    return
  fi

  local sorted=($(printf '%s\n' "${times[@]}" | sort -n))
  local count=${#sorted[@]}
  local p50=${sorted[$(( count / 2 ))]}
  local p99=${sorted[$(( count * 99 / 100 ))]}

  echo "{\"name\":\"$name\",\"p50_ms\":$p50,\"p99_ms\":$p99}"
}

# Ding
DING_CFG=$(mktemp /tmp/ding-startup-XXXXXX.yaml)
cat > "$DING_CFG" << 'EOF'
server:
  port: 8083
rules:
  - name: test
    metric: cpu_usage
    condition: value > 95
    alert:
      - notifier: stdout
EOF
ding=$(time_to_healthy "ding" \
  "./ding serve --config \"$DING_CFG\"" \
  "http://localhost:8083/health" \
  "kill")

# Prometheus (cold — run in foreground so kill $pid propagates to container)
# Pre-clean any leftover containers occupying port 9090
docker rm -f $(docker ps -q --filter "publish=9090") > /dev/null 2>&1 || true
prometheus_cold=$(time_to_healthy "prometheus_cold" \
  "docker run --rm -p 9090:9090 -v $(pwd)/benchmarks/comparison/prometheus.yml:/etc/prometheus/prometheus.yml:ro prom/prometheus:v2.51.0" \
  "http://localhost:9090/-/healthy" \
  "kill")

rm -f "$DING_CFG"

jq -n \
  --argjson ding "$ding" \
  --argjson prom "$prometheus_cold" \
  '{"ding":$ding,"prometheus":$prom}'
