package notifier

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/zuchka/ding/internal/evaluator"
)

// AlertLogger writes every fired alert as a JSON line to a file.
// Thread-safe. Construct with NewAlertLogger.
type AlertLogger struct {
	mu sync.Mutex
	f  *os.File
}

// NewAlertLogger opens (or creates) path for append-only JSON-line writes.
func NewAlertLogger(path string) (*AlertLogger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening alert log %q: %w", path, err)
	}
	return &AlertLogger{f: f}, nil
}

// Log serializes alert to JSON and appends it as a single line.
func (l *AlertLogger) Log(alert evaluator.Alert) error {
	payload := buildPayload(alert)
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling alert log entry: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = fmt.Fprintf(l.f, "%s\n", data)
	return err
}

// Close flushes and closes the underlying file.
func (l *AlertLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
