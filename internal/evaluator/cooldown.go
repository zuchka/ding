package evaluator

import (
	"sync"
	"time"
)

// CooldownTracker tracks per-(rule, label-set) cooldown expiry times.
type CooldownTracker struct {
	mu     sync.Mutex
	expiry map[string]time.Time
}

// NewCooldownTracker creates a new CooldownTracker.
func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{expiry: make(map[string]time.Time)}
}

// IsActive returns true if the cooldown for (rule, labelKey) is still active.
func (ct *CooldownTracker) IsActive(rule, labelKey string) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	exp, ok := ct.expiry[rule+":"+labelKey]
	return ok && time.Now().Before(exp)
}

// Set activates a cooldown for (rule, labelKey) for the given duration.
func (ct *CooldownTracker) Set(rule, labelKey string, d time.Duration) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.expiry[rule+":"+labelKey] = time.Now().Add(d)
}

// RemainingString returns a human-readable string of remaining cooldown time.
func (ct *CooldownTracker) RemainingString(rule, labelKey string) string {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	exp, ok := ct.expiry[rule+":"+labelKey]
	if !ok || time.Now().After(exp) {
		return "ready"
	}
	remaining := time.Until(exp).Round(time.Second)
	return remaining.String() + " remaining"
}
