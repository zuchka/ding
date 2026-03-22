package ingester

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// ParseJSONLine parses a single JSON object line into one Event.
func ParseJSONLine(data []byte) ([]Event, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	metricRaw, ok := raw["metric"]
	if !ok {
		return nil, fmt.Errorf("missing required field \"metric\"")
	}
	metric, ok := metricRaw.(string)
	if !ok || metric == "" {
		return nil, fmt.Errorf("\"metric\" must be a non-empty string")
	}

	valueRaw, ok := raw["value"]
	if !ok {
		return nil, fmt.Errorf("missing required field \"value\"")
	}
	value, ok := toFloat64(valueRaw)
	if !ok {
		return nil, fmt.Errorf("\"value\" must be a number")
	}

	at := time.Now()
	if tsRaw, ok := raw["timestamp"]; ok {
		if tsFloat, ok := toFloat64(tsRaw); ok {
			sec := int64(tsFloat)
			nsec := int64((tsFloat - float64(sec)) * 1e9)
			at = time.Unix(sec, nsec)
		}
	}

	labels := make(map[string]string)
	for k, v := range raw {
		if k == "metric" || k == "value" || k == "timestamp" {
			continue
		}
		labels[k] = fmt.Sprintf("%v", v)
	}

	return []Event{{Metric: metric, Value: value, Labels: labels, At: at}}, nil
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}
