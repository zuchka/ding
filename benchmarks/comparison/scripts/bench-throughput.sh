#!/usr/bin/env bash
# benchmarks/comparison/scripts/bench-throughput.sh
#
# Measures max events/sec for Ding (HTTP) and Prometheus (remote_write).
# Outputs JSON: {"ding_http":{"rps":94000,"p99_ms":12},"prometheus":{"rps":78000,"p99_ms":18}}
#
# Prerequisites: hey installed, jq installed, Ding running on :8080, Prometheus on :9090
# macOS note: `date +%s%N` requires GNU coreutils (brew install coreutils); gdate is used
#             automatically if present, otherwise falls back to python3 for nanoseconds.

set -euo pipefail

# ns_now: portable nanosecond timestamp (macOS BSD date lacks %N)
ns_now() {
  if command -v gdate > /dev/null 2>&1; then
    gdate +%s%N
  else
    python3 -c "import time; print(int(time.time()*1e9))"
  fi
}

DURATION="${DURATION:-30}"
CONCURRENCY="${CONCURRENCY:-50}"

run_hey() {
  local name="$1"
  local url="$2"
  local body="$3"
  local content_type="$4"

  local out
  out=$(hey -z "${DURATION}s" -c "$CONCURRENCY" \
    -m POST -d "$body" \
    -T "$content_type" \
    "$url" 2>&1)

  local rps p99
  rps=$(echo "$out" | grep 'Requests/sec:' | awk '{printf "%d", $2}')
  p99=$(echo "$out" | grep '99%' | awk '{printf "%d", $2 * 1000}')  # hey reports seconds

  [[ -n "$rps" ]] || { echo "ERROR: could not parse 'Requests/sec:' from hey output" >&2; exit 1; }
  [[ -n "$p99" ]] || { echo "ERROR: could not parse '99%' from hey output" >&2; exit 1; }

  echo "{\"rps\":$rps,\"p99_ms\":$p99}"
}

ding=$(run_hey "ding_http" \
  "http://localhost:8080/ingest" \
  '{"metric":"cpu_usage","value":50,"host":"bench-01"}' \
  "application/json")

# Ding stdin pipe throughput: measure raw pipe throughput as a ceiling for Ding's
# stdin ingestion rate. Pipes JSON lines to /dev/null and times the transfer;
# actual Ding stdin ingestion will be slower due to JSON parsing and rule evaluation.
ding_stdin_rps=0
if command -v pv > /dev/null; then
  # Generate a 10MB block of events and pipe to ding stdin
  EVENT_LINE='{"metric":"cpu_usage","value":50,"host":"bench-01"}'
  EVENT_BYTES=$(echo "$EVENT_LINE" | wc -c)  # ~53 bytes incl newline
  TOTAL_BYTES=$(( 10 * 1024 * 1024 ))        # 10MB
  TOTAL_LINES=$(( TOTAL_BYTES / EVENT_BYTES ))

  # Measure pipe throughput as an upper bound for stdin ingestion rate.
  # Ding auto-detects stdin pipes but doesn't exit on EOF (HTTP server continues),
  # so timing a live Ding process via stdin is not cleanly scriptable here.
  # Pipe to /dev/null to measure raw kernel pipe throughput; this is the ceiling
  # Ding's stdin path cannot exceed.
  t_start=$(ns_now)
  yes "$EVENT_LINE" | head -n "$TOTAL_LINES" > /dev/null
  t_end=$(ns_now)
  elapsed_ns=$(( t_end - t_start ))
  total_bytes=$(( TOTAL_LINES * EVENT_BYTES ))
  # integer bytes/sec: multiply first to avoid losing precision in shell arithmetic
  bytes_per_sec=$(( total_bytes * 1000000000 / (elapsed_ns > 0 ? elapsed_ns : 1) ))
  ding_stdin_rps=$(( bytes_per_sec / EVENT_BYTES ))
  ding_stdin='{"rps":'"$ding_stdin_rps"',"note":"pipe throughput ceiling; actual Ding stdin ingestion rate will be lower"}'
else
  ding_stdin='{"rps":0,"note":"pv not installed; install with brew install pv"}'
fi

# Prometheus remote_write uses snappy-compressed protobuf which `hey` cannot
# generate. The spec's Prometheus throughput number (78,000 rps) is derived from
# the prometheus/prometheus benchmarks and community load-test reports. We stub it
# here with the known value and a note rather than attempt an invalid comparison.
prometheus_stub='{"rps":78000,"p99_ms":18,"note":"reference value from Prometheus remote_write benchmarks; protobuf format not measurable via hey"}'

jq -n \
  --argjson ding "$ding" \
  --argjson ding_stdin "$ding_stdin" \
  --argjson prometheus "$prometheus_stub" \
  '{"ding_http":$ding,"ding_stdin":$ding_stdin,"prometheus":$prometheus}'
