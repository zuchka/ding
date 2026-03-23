#!/usr/bin/env bash
# benchmarks/comparison/scripts/bench-config-lines.sh
#
# Counts non-blank, non-comment config lines for each system.
# Outputs JSON: {"ding":{"files":1,"lines":12},"prometheus":{"files":3,"lines":47}}

set -euo pipefail

count_lines() {
  local file="$1"
  # Strip blank lines and lines starting with #
  grep -cv '^\s*$\|^\s*#' "$file" || true
}

# Ding: a minimal single-rule config
DING_CFG=$(mktemp /tmp/ding-cfg-XXXXXX.yaml)
cat > "$DING_CFG" << 'EOF'
rules:
  - name: high_cpu
    metric: cpu_usage
    condition: avg(value) over 5m > 80
    cooldown: 10m
    message: "CPU high on {{ .host }}: avg={{ .avg }}"
    notify:
      - webhook: https://hooks.example.com/alert
EOF
ding_lines=$(count_lines "$DING_CFG")

# Prometheus: three files
prom_lines=$(( \
  $(count_lines benchmarks/comparison/prometheus.yml) + \
  $(count_lines benchmarks/comparison/rules.yaml) + \
  $(count_lines benchmarks/comparison/alertmanager.yml) \
))

rm -f "$DING_CFG"

jq -n \
  --argjson ding_lines "$ding_lines" \
  --argjson prom_lines "$prom_lines" \
  '{"ding":{"files":1,"lines":$ding_lines},"prometheus":{"files":3,"lines":$prom_lines}}'
