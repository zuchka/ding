package notifier

import (
	"time"

	"github.com/super-ding/ding/internal/evaluator"
)

// Notifier sends a fired alert somewhere.
type Notifier interface {
	Send(alert evaluator.Alert) error
}

func buildPayload(alert evaluator.Alert) map[string]interface{} {
	p := map[string]interface{}{}
	// Labels first — reserved keys below will overwrite any collision
	for k, v := range alert.Labels {
		p[k] = v
	}
	// Reserved keys always win
	p["rule"] = alert.Rule
	p["message"] = alert.Message
	p["metric"] = alert.Metric
	p["value"] = alert.Value
	p["fired_at"] = alert.FiredAt.Format(time.RFC3339)
	return p
}
