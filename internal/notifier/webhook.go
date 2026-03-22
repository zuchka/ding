package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/super-ding/ding/internal/evaluator"
)

type retryItem struct {
	payload []byte
	rule    string
	attempt int       // number of failed delivery attempts so far (0 = never tried)
	nextAt  time.Time // earliest time to attempt delivery
}

// WebhookNotifier POSTs alert payloads to an HTTP endpoint with exponential backoff retry.
// Queue capacity 256: at initialBackoff=1s with maxAttempts=3, a queue of 256 items drains
// in roughly 7s under full failure. Under sustained high alert volume with a failing webhook,
// the queue saturates quickly and drops are logged — this is the intended fail-fast behavior.
type WebhookNotifier struct {
	url            string
	client         *http.Client
	maxAttempts    int
	initialBackoff time.Duration
	queue          chan retryItem
	stop           chan struct{}
	stopOnce       sync.Once
}

// NewWebhookNotifier creates and starts a WebhookNotifier.
func NewWebhookNotifier(url string, maxAttempts int, initialBackoff time.Duration) *WebhookNotifier {
	n := &WebhookNotifier{
		url:            url,
		client:         &http.Client{Timeout: 10 * time.Second},
		maxAttempts:    maxAttempts,
		initialBackoff: initialBackoff,
		queue:          make(chan retryItem, 256),
		stop:           make(chan struct{}),
	}
	go n.worker()
	return n
}

// Send enqueues an alert for delivery. Returns nil always (Notifier interface contract).
// If the queue is full, the alert is dropped and a warning is logged.
func (n *WebhookNotifier) Send(alert evaluator.Alert) error {
	payload := buildPayload(alert)
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("ding: webhook marshal error for rule %q: %v", alert.Rule, err)
		return nil
	}
	item := retryItem{
		payload: data,
		rule:    alert.Rule,
		attempt: 0,
		nextAt:  time.Now(),
	}
	select {
	case n.queue <- item:
	default:
		log.Printf("ding: webhook queue full for rule %q, dropping alert", alert.Rule)
	}
	return nil
}

// Stop signals the worker to exit. Safe to call multiple times.
func (n *WebhookNotifier) Stop() {
	n.stopOnce.Do(func() { close(n.stop) })
}

func (n *WebhookNotifier) worker() {
	for {
		select {
		case <-n.stop:
			return
		case item := <-n.queue:
			delay := time.Until(item.nextAt)
			if delay > 0 {
				t := time.NewTimer(delay)
				select {
				case <-t.C:
				case <-n.stop:
					// Follow the standard Go timer drain idiom.
					if !t.Stop() {
						<-t.C
					}
					return
				}
			}
			if err := n.deliver(item); err != nil {
				item.attempt++
				if item.attempt >= n.maxAttempts {
					log.Printf("ding: webhook dropped after %d attempts for rule %q: %v", n.maxAttempts, item.rule, err)
					continue
				}
				// Backoff: initialBackoff * 2^(attempt-1)
				// attempt=1 → initialBackoff*1, attempt=2 → initialBackoff*2, etc.
				backoff := n.initialBackoff * (1 << (item.attempt - 1))
				item.nextAt = time.Now().Add(backoff)
				select {
				case n.queue <- item:
				default:
					log.Printf("ding: webhook queue full during retry for rule %q, dropping", item.rule)
				}
			}
		}
	}
}

// deliver performs a single HTTP POST. Returns an error for 5xx or connection errors (retryable).
// Returns nil for 2xx/3xx (success) and 4xx (not retryable — logged and discarded).
func (n *WebhookNotifier) deliver(item retryItem) error {
	resp, err := n.client.Post(n.url, "application/json", bytes.NewReader(item.payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drain body to allow connection reuse; cap to avoid unexpected large responses.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		log.Printf("ding: webhook %s returned %d for rule %q (not retrying)", n.url, resp.StatusCode, item.rule)
	}
	return nil
}
