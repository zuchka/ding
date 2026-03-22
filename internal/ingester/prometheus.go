package ingester

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reMetricWithLabels = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+([0-9eE+\-.]+)`)
	reMetricNoLabels   = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\s+([0-9eE+\-.]+)`)
	reLabel            = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="([^"]*)"`)
)

// ParsePrometheusText parses Prometheus text exposition format into Events.
func ParsePrometheusText(data []byte) ([]Event, error) {
	var events []Event
	scanner := bufio.NewScanner(bytes.NewReader(data))
	now := time.Now()

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if m := reMetricWithLabels.FindStringSubmatch(line); m != nil {
			name := m[1]
			labelStr := m[2]
			valueStr := m[3]

			v, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q in line: %s", valueStr, line)
			}

			labels := make(map[string]string)
			for _, lm := range reLabel.FindAllStringSubmatch(labelStr, -1) {
				labels[lm[1]] = lm[2]
			}

			events = append(events, Event{Metric: name, Value: v, Labels: labels, At: now})
			continue
		}

		if m := reMetricNoLabels.FindStringSubmatch(line); m != nil {
			v, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q in line: %s", m[2], line)
			}
			events = append(events, Event{
				Metric: m[1], Value: v, Labels: make(map[string]string), At: now,
			})
			continue
		}

		return nil, fmt.Errorf("unrecognized Prometheus line: %s", line)
	}

	return events, scanner.Err()
}
