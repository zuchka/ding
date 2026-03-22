package evaluator_test

import (
	"testing"
	"time"

	"github.com/super-ding/ding/internal/evaluator"
	"github.com/super-ding/ding/internal/ingester"
)

// ---- Condition parsing ----

func TestParseCondition_EventPerEvent(t *testing.T) {
	cases := []struct {
		input string
		op    string
		lit   float64
	}{
		{"value > 95", ">", 95},
		{"value >= 80.5", ">=", 80.5},
		{"value < 0", "<", 0},
		{"value == 42", "==", 42},
		{"value != 0", "!=", 0},
	}
	for _, tc := range cases {
		c, err := evaluator.ParseCondition(tc.input)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.input, err)
			continue
		}
		if c.Windowed {
			t.Errorf("%q: expected event-per-event, got windowed", tc.input)
		}
		if c.Op != tc.op || c.Literal != tc.lit {
			t.Errorf("%q: got op=%s lit=%f", tc.input, c.Op, c.Literal)
		}
	}
}

func TestParseCondition_Windowed(t *testing.T) {
	c, err := evaluator.ParseCondition("avg(value) over 5m > 80")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Windowed {
		t.Fatal("expected windowed condition")
	}
	if c.Func != "avg" || c.Window != 5*time.Minute || c.Op != ">" || c.Literal != 80 {
		t.Errorf("unexpected condition: %+v", c)
	}
}

func TestParseCondition_Invalid(t *testing.T) {
	_, err := evaluator.ParseCondition("value OVER 95")
	if err == nil {
		t.Fatal("expected error for invalid condition")
	}
}

// ---- Matcher ----

func TestMatch_MetricOnly(t *testing.T) {
	rule := evaluator.MatchRule{Match: map[string]string{"metric": "cpu_usage"}}
	event := ingester.Event{Metric: "cpu_usage", Labels: map[string]string{"host": "web-01"}}

	if !evaluator.Match(event, rule) {
		t.Error("expected match")
	}
}

func TestMatch_MetricAndLabel(t *testing.T) {
	rule := evaluator.MatchRule{Match: map[string]string{"metric": "cpu_usage", "host": "web-01"}}
	eventMatch := ingester.Event{Metric: "cpu_usage", Labels: map[string]string{"host": "web-01"}}
	eventNoMatch := ingester.Event{Metric: "cpu_usage", Labels: map[string]string{"host": "web-02"}}

	if !evaluator.Match(eventMatch, rule) {
		t.Error("expected match for web-01")
	}
	if evaluator.Match(eventNoMatch, rule) {
		t.Error("expected no match for web-02")
	}
}

func TestMatch_EmptyMatchBlock(t *testing.T) {
	rule := evaluator.MatchRule{Match: nil}
	event := ingester.Event{Metric: "anything", Labels: map[string]string{}}
	if !evaluator.Match(event, rule) {
		t.Error("empty match block should match all events")
	}
}

// ---- Ring buffer ----

func TestRingBuffer_EvictsOldEntries(t *testing.T) {
	rb := evaluator.NewRingBuffer(5*time.Minute, 1000)
	now := time.Now()

	rb.Add(10.0, now.Add(-6*time.Minute)) // old, should be evicted
	rb.Add(20.0, now.Add(-1*time.Minute)) // recent
	rb.Add(30.0, now)                     // recent

	avg := rb.Avg(now)
	// only the two recent entries (20+30)/2 = 25
	if avg != 25.0 {
		t.Errorf("expected avg 25, got %f", avg)
	}
}

func TestRingBuffer_MaxSize(t *testing.T) {
	rb := evaluator.NewRingBuffer(5*time.Minute, 3)
	now := time.Now()
	rb.Add(1.0, now)
	rb.Add(2.0, now)
	rb.Add(3.0, now)
	rb.Add(4.0, now) // should evict first entry

	if rb.Count(now) != 3 {
		t.Errorf("expected count 3, got %d", int(rb.Count(now)))
	}
}

func TestRingBuffer_EmptyReturnsZeroAndFalse(t *testing.T) {
	rb := evaluator.NewRingBuffer(5*time.Minute, 1000)
	now := time.Now()
	if rb.HasEntries(now) {
		t.Error("empty buffer should have no entries")
	}
}

func TestRingBuffer_Aggregates(t *testing.T) {
	rb := evaluator.NewRingBuffer(5*time.Minute, 1000)
	now := time.Now()
	for _, v := range []float64{10, 20, 30, 40, 50} {
		rb.Add(v, now)
	}
	if rb.Avg(now) != 30 {
		t.Errorf("avg: expected 30, got %f", rb.Avg(now))
	}
	if rb.Max(now) != 50 {
		t.Errorf("max: expected 50, got %f", rb.Max(now))
	}
	if rb.Min(now) != 10 {
		t.Errorf("min: expected 10, got %f", rb.Min(now))
	}
	if rb.Sum(now) != 150 {
		t.Errorf("sum: expected 150, got %f", rb.Sum(now))
	}
	if rb.Count(now) != 5 {
		t.Errorf("count: expected 5, got %f", rb.Count(now))
	}
}
