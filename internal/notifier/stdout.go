package notifier

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/super-ding/ding/internal/evaluator"
)

// StdoutNotifier writes alerts as JSON lines to a writer (default: os.Stdout).
type StdoutNotifier struct {
	w io.Writer
}

// NewStdoutNotifier creates a notifier that writes to w (pass nil for os.Stdout).
func NewStdoutNotifier(w io.Writer) *StdoutNotifier {
	if w == nil {
		w = os.Stdout
	}
	return &StdoutNotifier{w: w}
}

func (n *StdoutNotifier) Send(alert evaluator.Alert) error {
	payload := buildPayload(alert)
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling alert: %w", err)
	}
	_, err = fmt.Fprintf(n.w, "%s\n", data)
	return err
}

func buildPayload(alert evaluator.Alert) map[string]interface{} {
	p := map[string]interface{}{
		"rule":     alert.Rule,
		"message":  alert.Message,
		"metric":   alert.Metric,
		"value":    alert.Value,
		"fired_at": alert.FiredAt.Format(time.RFC3339),
	}
	for k, v := range alert.Labels {
		p[k] = v
	}
	return p
}
