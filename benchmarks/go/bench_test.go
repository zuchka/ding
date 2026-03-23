// benchmarks/go/bench_test.go
package bench_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/super-ding/ding/internal/evaluator"
	"github.com/super-ding/ding/internal/ingester"
)

// BenchmarkProcessSimpleRule measures throughput of Engine.Process() with a
// single event-per-event threshold rule. Value is below threshold so no alert
// fires — measures pure evaluation cost, not notifier dispatch.
func BenchmarkProcessSimpleRule(b *testing.B) {
	engine, err := evaluator.NewEngine([]evaluator.EngineRule{
		{
			Name:      "high_cpu",
			Condition: "value > 95",
			Cooldown:  0,
			Alerts:    []string{},
		},
	}, 10000)
	if err != nil {
		b.Fatal(err)
	}
	event := ingester.Event{
		Metric: "cpu_usage",
		Value:  50.0, // below threshold: no alert fires, measures eval path only
		Labels: map[string]string{"host": "web-01"},
		At:     time.Now(),
	}
	now := time.Now()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		engine.Process(event, now)
	}
}

// BenchmarkProcessWindowedRule measures throughput with a windowed rule.
// Each Process() call appends to a ring buffer then scans O(n) entries for the
// aggregate — this is the more expensive path.
func BenchmarkProcessWindowedRule(b *testing.B) {
	engine, err := evaluator.NewEngine([]evaluator.EngineRule{
		{
			Name:      "high_cpu_avg",
			Condition: "avg(value) over 5m > 80",
			Cooldown:  0,
			Alerts:    []string{},
		},
	}, 10000)
	if err != nil {
		b.Fatal(err)
	}
	event := ingester.Event{
		Metric: "cpu_usage",
		Value:  50.0, // below threshold
		Labels: map[string]string{"host": "web-01"},
		At:     time.Now(),
	}
	now := time.Now()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		engine.Process(event, now)
	}
}

// BenchmarkProcess100Rules measures throughput when 100 rules are active —
// the full rule-scan loop overhead. Each event is evaluated against all 100
// rules; none fire (value below threshold).
func BenchmarkProcess100Rules(b *testing.B) {
	rules := make([]evaluator.EngineRule, 100)
	for i := range rules {
		rules[i] = evaluator.EngineRule{
			Name:      fmt.Sprintf("rule_%03d", i),
			Condition: "avg(value) over 5m > 80",
			Cooldown:  0,
			Alerts:    []string{},
		}
	}
	engine, err := evaluator.NewEngine(rules, 10000)
	if err != nil {
		b.Fatal(err)
	}
	event := ingester.Event{
		Metric: "cpu_usage",
		Value:  50.0,
		Labels: map[string]string{"host": "web-01"},
		At:     time.Now(),
	}
	now := time.Now()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		engine.Process(event, now)
	}
}

// BenchmarkEngineInit measures the cost of creating a new Engine with 100 rules —
// this is the hot path during both cold start and hot-reload (new engine is built
// before the old one is swapped out).
func BenchmarkEngineInit(b *testing.B) {
	rules := make([]evaluator.EngineRule, 100)
	for i := range rules {
		rules[i] = evaluator.EngineRule{
			Name:      fmt.Sprintf("rule_%03d", i),
			Condition: "avg(value) over 5m > 80",
			Cooldown:  0,
			Alerts:    []string{},
		}
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := evaluator.NewEngine(rules, 10000)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEngineSwap measures the cost of creating a replacement engine —
// simulates the hot-reload code path where a new engine is built from the
// same rule set and then atomically swapped in. The swap itself (mutex Lock)
// is O(1); the cost is dominated by NewEngine parsing all rules.
func BenchmarkEngineSwap(b *testing.B) {
	rules := make([]evaluator.EngineRule, 10)
	for i := range rules {
		rules[i] = evaluator.EngineRule{
			Name:      fmt.Sprintf("rule_%02d", i),
			Condition: "value > 80",
			Cooldown:  0,
			Alerts:    []string{},
		}
	}
	engine, err := evaluator.NewEngine(rules, 10000)
	if err != nil {
		b.Fatal(err)
	}
	_ = engine // original engine; swap target is a new one built each iteration
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		newEngine, err := evaluator.NewEngine(rules, 10000)
		if err != nil {
			b.Fatal(err)
		}
		_ = newEngine // in production, server.mu.Lock(); server.engine = newEngine
	}
}
