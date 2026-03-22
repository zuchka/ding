package evaluator

import (
	"sync"
	"time"
)

type entry struct {
	value float64
	at    time.Time
}

// RingBuffer is a time-based sliding window of float64 values.
// Thread-safe.
type RingBuffer struct {
	mu      sync.Mutex
	entries []entry
	window  time.Duration
	maxSize int
}

// NewRingBuffer creates a new RingBuffer with the given window and max size.
func NewRingBuffer(window time.Duration, maxSize int) *RingBuffer {
	return &RingBuffer{window: window, maxSize: maxSize}
}

// Add inserts a new value observed at time at.
func (rb *RingBuffer) Add(value float64, at time.Time) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.evict(at)
	rb.entries = append(rb.entries, entry{value: value, at: at})
	if len(rb.entries) > rb.maxSize {
		rb.entries = rb.entries[1:]
	}
}

// evict removes entries older than the window. Must be called with lock held.
func (rb *RingBuffer) evict(now time.Time) {
	cutoff := now.Add(-rb.window)
	i := 0
	for i < len(rb.entries) && rb.entries[i].at.Before(cutoff) {
		i++
	}
	rb.entries = rb.entries[i:]
}

// HasEntries returns true if there are any entries in the window.
func (rb *RingBuffer) HasEntries(now time.Time) bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.evict(now)
	return len(rb.entries) > 0
}

// Avg returns the average of all values in the window (0 if empty).
func (rb *RingBuffer) Avg(now time.Time) float64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.evict(now)
	if len(rb.entries) == 0 {
		return 0
	}
	sum := 0.0
	for _, e := range rb.entries {
		sum += e.value
	}
	return sum / float64(len(rb.entries))
}

// Max returns the maximum value in the window (0 if empty).
func (rb *RingBuffer) Max(now time.Time) float64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.evict(now)
	if len(rb.entries) == 0 {
		return 0
	}
	max := rb.entries[0].value
	for _, e := range rb.entries[1:] {
		if e.value > max {
			max = e.value
		}
	}
	return max
}

// Min returns the minimum value in the window (0 if empty).
func (rb *RingBuffer) Min(now time.Time) float64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.evict(now)
	if len(rb.entries) == 0 {
		return 0
	}
	min := rb.entries[0].value
	for _, e := range rb.entries[1:] {
		if e.value < min {
			min = e.value
		}
	}
	return min
}

// Sum returns the sum of all values in the window.
func (rb *RingBuffer) Sum(now time.Time) float64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.evict(now)
	sum := 0.0
	for _, e := range rb.entries {
		sum += e.value
	}
	return sum
}

// Count returns the number of events in the window. The value argument is ignored.
func (rb *RingBuffer) Count(now time.Time) float64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.evict(now)
	return float64(len(rb.entries))
}
