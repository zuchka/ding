package evaluator

import (
	"bytes"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/super-ding/ding/internal/ingester"
)

// EngineRule is the fully-resolved rule fed to the Engine.
type EngineRule struct {
	Name      string
	Match     map[string]string
	Condition string
	Cooldown  time.Duration
	Message   string
	Alerts    []string // notifier names ("stdout" or named webhook)
}

// Alert is a fired alert ready to dispatch.
type Alert struct {
	Rule      string
	Message   string
	Metric    string
	Value     float64
	Labels    map[string]string
	FiredAt   time.Time
	Notifiers []string
	// Aggregate values for windowed rules (zero if event-per-event)
	Avg   float64
	Max   float64
	Min   float64
	Count float64
	Sum   float64
}

// Engine evaluates events against rules and produces alerts.
// Thread-safe. Supports atomic hot-swap via Swap().
//
// Locking strategy:
//   - mu (RWMutex): protects rules/maxBuf during hot-reload. Process() holds RLock;
//     SwapEngine() holds Lock.
//   - bufMu (Mutex): protects buffers and seenLabelKeys independently of mu, so
//     buffer creation never races with concurrent Process() calls.
type Engine struct {
	mu            sync.RWMutex
	bufMu         sync.Mutex
	rules         []parsedRule
	buffers       map[string]*RingBuffer // keyed by "ruleName:labelSetKey"
	seenLabelKeys map[string][]string   // ruleName -> seen label-set keys (for /rules endpoint)
	cooldown      *CooldownTracker
	maxBuf        int
}

type parsedRule struct {
	EngineRule
	cond Condition
}

// NewEngine creates an Engine from a slice of EngineRules.
func NewEngine(rules []EngineRule, maxBufferSize int) (*Engine, error) {
	parsed := make([]parsedRule, len(rules))
	for i, r := range rules {
		c, err := ParseCondition(r.Condition)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		parsed[i] = parsedRule{EngineRule: r, cond: c}
	}
	return &Engine{
		rules:         parsed,
		buffers:       make(map[string]*RingBuffer),
		seenLabelKeys: make(map[string][]string),
		cooldown:      NewCooldownTracker(),
		maxBuf:        maxBufferSize,
	}, nil
}

// Process evaluates an event against all rules. Returns fired alerts.
func (e *Engine) Process(event ingester.Event, now time.Time) []Alert {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var alerts []Alert
	for _, rule := range e.rules {
		mr := MatchRule{Match: rule.Match}
		if !Match(event, mr) {
			continue
		}

		labelKey := LabelSetKey(event.Labels)
		bufKey := rule.Name + ":" + labelKey

		// Track seen label keys for /rules endpoint (before condition check)
		e.trackLabelKey(rule.Name, labelKey)

		var aggregates map[string]float64
		if rule.cond.Windowed {
			buf := e.getOrCreateBuffer(bufKey, rule.cond.Window)
			buf.Add(event.Value, event.At)
			if !buf.HasEntries(now) {
				continue
			}
			aggregates = map[string]float64{
				"avg":   buf.Avg(now),
				"max":   buf.Max(now),
				"min":   buf.Min(now),
				"count": buf.Count(now),
				"sum":   buf.Sum(now),
			}
			agg := aggregates[rule.cond.Func]
			if !applyOp(agg, rule.cond.Op, rule.cond.Literal) {
				continue
			}
		} else {
			if !applyOp(event.Value, rule.cond.Op, rule.cond.Literal) {
				continue
			}
		}

		if e.cooldown.IsActive(rule.Name, labelKey) {
			continue
		}
		if rule.Cooldown > 0 {
			e.cooldown.Set(rule.Name, labelKey, rule.Cooldown)
		}

		alert := Alert{
			Rule:      rule.Name,
			Metric:    event.Metric,
			Value:     event.Value,
			Labels:    event.Labels,
			FiredAt:   now,
			Notifiers: rule.Alerts,
		}
		if aggregates != nil {
			alert.Avg = aggregates["avg"]
			alert.Max = aggregates["max"]
			alert.Min = aggregates["min"]
			alert.Count = aggregates["count"]
			alert.Sum = aggregates["sum"]
		}
		alert.Message = renderMessage(rule.Message, alert)
		alerts = append(alerts, alert)
	}
	return alerts
}

// RulesStatus returns rule names with their cooldown states.
func (e *Engine) RulesStatus() []RuleStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Snapshot seenLabelKeys under bufMu
	e.bufMu.Lock()
	seenCopy := make(map[string][]string, len(e.seenLabelKeys))
	for k, v := range e.seenLabelKeys {
		cp := make([]string, len(v))
		copy(cp, v)
		seenCopy[k] = cp
	}
	e.bufMu.Unlock()

	out := make([]RuleStatus, len(e.rules))
	for i, r := range e.rules {
		cooling := make(map[string]string)
		for _, labelKey := range seenCopy[r.Name] {
			cooling[labelKey] = e.cooldown.RemainingString(r.Name, labelKey)
		}
		out[i] = RuleStatus{
			Name:        r.Name,
			Condition:   r.Condition,
			Cooldown:    r.Cooldown.String(),
			CoolingDown: cooling,
		}
	}
	return out
}

// RuleStatus is used by the /rules HTTP endpoint.
type RuleStatus struct {
	Name        string
	Condition   string
	Cooldown    string
	CoolingDown map[string]string
}

// trackLabelKey records that a label-set key was seen for a rule.
func (e *Engine) trackLabelKey(ruleName, labelKey string) {
	e.bufMu.Lock()
	defer e.bufMu.Unlock()
	for _, k := range e.seenLabelKeys[ruleName] {
		if k == labelKey {
			return
		}
	}
	e.seenLabelKeys[ruleName] = append(e.seenLabelKeys[ruleName], labelKey)
}

// getOrCreateBuffer returns the ring buffer for a buffer key, creating it if needed.
// Uses bufMu independently of the RWMutex so it is safe to call from Process() under RLock.
func (e *Engine) getOrCreateBuffer(key string, window time.Duration) *RingBuffer {
	e.bufMu.Lock()
	defer e.bufMu.Unlock()
	if buf, ok := e.buffers[key]; ok {
		return buf
	}
	buf := NewRingBuffer(window, e.maxBuf)
	e.buffers[key] = buf
	return buf
}

// LabelSetKey serializes a label map to a canonical sorted string.
func LabelSetKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ",")
}

// StartFlusher starts a background goroutine that periodically saves engine state to path.
// Returns a stop function that triggers a final flush and blocks until it completes.
// The caller should initialize stopFlusher to a no-op before calling this, so shutdown
// can call it unconditionally: var stopFlusher func() = func() {}
func (e *Engine) StartFlusher(path string, interval time.Duration) func() {
	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snap := SnapshotEngine(e)
				if err := SaveSnapshot(path, snap); err != nil {
					log.Printf("ding: state flush failed: %v", err)
				}
			case <-stop:
				snap := SnapshotEngine(e)
				if err := SaveSnapshot(path, snap); err != nil {
					log.Printf("ding: final state flush failed: %v", err)
				}
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

func renderMessage(tmpl string, alert Alert) string {
	if tmpl == "" {
		return fmt.Sprintf("rule %q fired (metric=%s value=%v)", alert.Rule, alert.Metric, alert.Value)
	}
	t, err := template.New("msg").Parse(tmpl)
	if err != nil {
		return tmpl // return raw if template is invalid
	}
	data := map[string]interface{}{
		"metric":   alert.Metric,
		"value":    alert.Value,
		"rule":     alert.Rule,
		"fired_at": alert.FiredAt.Format(time.RFC3339),
		"avg":      alert.Avg,
		"max":      alert.Max,
		"min":      alert.Min,
		"count":    alert.Count,
		"sum":      alert.Sum,
	}
	for k, v := range alert.Labels {
		data[k] = v
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return tmpl
	}
	return buf.String()
}
