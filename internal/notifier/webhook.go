package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/super-ding/ding/internal/evaluator"
)

// WebhookNotifier POSTs alert payloads to an HTTP endpoint.
type WebhookNotifier struct {
	url    string
	client *http.Client
}

// NewWebhookNotifier creates a notifier that POSTs to url.
func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *WebhookNotifier) Send(alert evaluator.Alert) error {
	payload := buildPayload(alert)
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling webhook payload: %w", err)
	}

	resp, err := n.client.Post(n.url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("ding: webhook delivery failed for rule %q: %v", alert.Rule, err)
		return nil // silent failure — no retry in v1
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("ding: webhook %s returned %d for rule %q", n.url, resp.StatusCode, alert.Rule)
	}
	return nil
}
