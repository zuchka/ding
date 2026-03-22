package notifier

import "github.com/super-ding/ding/internal/evaluator"

// Notifier sends a fired alert somewhere.
type Notifier interface {
	Send(alert evaluator.Alert) error
}
