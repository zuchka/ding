package evaluator

import "github.com/super-ding/ding/internal/ingester"

// MatchRule is the minimal match configuration needed by the matcher.
type MatchRule struct {
	Match map[string]string
}

// Match returns true if the event satisfies all key=value pairs in the match block.
// If match is nil or empty, matches all events.
func Match(event ingester.Event, rule MatchRule) bool {
	for k, v := range rule.Match {
		if k == "metric" {
			if event.Metric != v {
				return false
			}
			continue
		}
		if event.Labels[k] != v {
			return false
		}
	}
	return true
}
