package evaluator_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zuchka/ding/internal/evaluator"
	"github.com/zuchka/ding/internal/ingester"
)

// helper: create a windowed engine with a single rule
func makeWindowedEngine(t *testing.T) *evaluator.Engine {
	t.Helper()
	rules := []evaluator.EngineRule{
		{
			Name:      "cpu_sustained",
			Match:     map[string]string{"metric": "cpu_usage"},
			Condition: "avg(value) over 5m > 80",
			Cooldown:  0,
			Message:   "Sustained CPU",
			Alerts:    []string{"stdout"},
		},
	}
	eng, err := evaluator.NewEngine(rules, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// helper: create an engine with cooldown rule
func makeCooldownEngine(t *testing.T) *evaluator.Engine {
	t.Helper()
	rules := []evaluator.EngineRule{
		{
			Name:      "cpu_spike",
			Match:     map[string]string{"metric": "cpu_usage"},
			Condition: "value > 90",
			Cooldown:  5 * time.Minute,
			Message:   "CPU spike",
			Alerts:    []string{"stdout"},
		},
	}
	eng, err := evaluator.NewEngine(rules, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// TestSnapshotEngine_Empty verifies that a fresh engine produces empty maps.
func TestSnapshotEngine_Empty(t *testing.T) {
	eng := makeWindowedEngine(t)
	snap := evaluator.SnapshotEngine(eng)

	if snap.Version != 1 {
		t.Errorf("expected version 1, got %d", snap.Version)
	}
	if len(snap.Buffers) != 0 {
		t.Errorf("expected empty Buffers, got %d entries", len(snap.Buffers))
	}
	if len(snap.Cooldowns) != 0 {
		t.Errorf("expected empty Cooldowns, got %d entries", len(snap.Cooldowns))
	}
}

// TestSnapshotEngine_CapturesBuffers verifies that processed events appear in the snapshot.
func TestSnapshotEngine_CapturesBuffers(t *testing.T) {
	eng := makeWindowedEngine(t)
	now := time.Now()
	event := ingester.Event{
		Metric: "cpu_usage",
		Value:  85,
		Labels: map[string]string{"host": "web-01"},
		At:     now,
	}
	eng.Process(event, now)

	snap := evaluator.SnapshotEngine(eng)

	if len(snap.Buffers) == 0 {
		t.Fatal("expected at least one buffer in snapshot")
	}

	// The key should be "cpu_sustained:host=web-01"
	expectedKey := "cpu_sustained:host=web-01"
	bs, ok := snap.Buffers[expectedKey]
	if !ok {
		t.Fatalf("expected buffer key %q in snapshot, got keys: %v", expectedKey, keysOf(snap.Buffers))
	}
	if len(bs.Entries) == 0 {
		t.Fatal("expected entries in buffer snapshot")
	}
	if bs.Entries[0].Value != 85 {
		t.Errorf("expected entry value 85, got %f", bs.Entries[0].Value)
	}
}

// TestSnapshotEngine_CapturesCooldowns verifies that a triggered cooldown appears in the snapshot.
func TestSnapshotEngine_CapturesCooldowns(t *testing.T) {
	eng := makeCooldownEngine(t)
	now := time.Now()
	event := ingester.Event{
		Metric: "cpu_usage",
		Value:  97,
		Labels: map[string]string{"host": "web-01"},
		At:     now,
	}
	alerts := eng.Process(event, now)
	if len(alerts) == 0 {
		t.Fatal("expected alert to fire so cooldown is set")
	}

	snap := evaluator.SnapshotEngine(eng)

	if len(snap.Cooldowns) == 0 {
		t.Fatal("expected cooldowns in snapshot")
	}

	// Cooldown key format: "ruleName:labelKey"
	expectedKey := "cpu_spike:host=web-01"
	exp, ok := snap.Cooldowns[expectedKey]
	if !ok {
		t.Fatalf("expected cooldown key %q, got: %v", expectedKey, snap.Cooldowns)
	}
	if !exp.After(now) {
		t.Errorf("expected cooldown expiry to be in the future, got %v", exp)
	}
}

// TestRestoreEngine_RejectsExpiredEntries verifies that old buffer entries are dropped on restore.
func TestRestoreEngine_RejectsExpiredEntries(t *testing.T) {
	snap := evaluator.StateSnapshot{
		Version: 1,
		SavedAt: time.Now().UTC(),
		Buffers: map[string]evaluator.BufferSnapshot{
			"cpu_sustained:host=web-01": {
				Window:  5 * time.Minute,
				MaxSize: 1000,
				Entries: []evaluator.EntrySnapshot{
					{Value: 90, At: time.Now().Add(-10 * time.Minute)}, // older than 5m window
				},
			},
		},
		Cooldowns: map[string]time.Time{},
	}

	eng := makeWindowedEngine(t)
	evaluator.RestoreEngine(eng, snap, time.Now())

	// After restore, buffer should be empty because the entry is outside the window.
	// Verify by snapshotting the new engine — its buffers should be empty.
	newSnap := evaluator.SnapshotEngine(eng)
	if len(newSnap.Buffers) != 0 {
		t.Errorf("expected no buffers after restoring expired entries, got %d", len(newSnap.Buffers))
	}
}

// TestRestoreEngine_RejectsExpiredCooldowns verifies that expired cooldowns are not restored,
// so the engine fires again on matching events.
func TestRestoreEngine_RejectsExpiredCooldowns(t *testing.T) {
	now := time.Now()
	snap := evaluator.StateSnapshot{
		Version: 1,
		SavedAt: now.UTC(),
		Buffers: map[string]evaluator.BufferSnapshot{},
		Cooldowns: map[string]time.Time{
			"cpu_spike:host=web-01": now.Add(-1 * time.Minute), // expired 1 minute ago
		},
	}

	eng := makeCooldownEngine(t)
	evaluator.RestoreEngine(eng, snap, now)

	// Since the cooldown was expired, the engine should fire on a matching event
	event := ingester.Event{
		Metric: "cpu_usage",
		Value:  97,
		Labels: map[string]string{"host": "web-01"},
		At:     now,
	}
	alerts := eng.Process(event, now)
	if len(alerts) == 0 {
		t.Error("expected alert to fire because expired cooldown should not have been restored")
	}
}

// TestRestoreEngine_PreservesActiveState verifies that active state is correctly transferred.
func TestRestoreEngine_PreservesActiveState(t *testing.T) {
	eng1 := makeWindowedEngine(t)
	now := time.Now()

	// Feed several events into eng1
	for _, v := range []float64{85, 90, 87, 92, 91} {
		eng1.Process(ingester.Event{
			Metric: "cpu_usage",
			Value:  v,
			Labels: map[string]string{"host": "web-01"},
			At:     now,
		}, now)
	}

	// Snapshot eng1
	snap := evaluator.SnapshotEngine(eng1)

	// Restore into a fresh engine with same rules
	eng2 := makeWindowedEngine(t)
	evaluator.RestoreEngine(eng2, snap, now)

	// eng2 should have the same buffers as eng1
	snap2 := evaluator.SnapshotEngine(eng2)

	key := "cpu_sustained:host=web-01"
	bs1, ok1 := snap.Buffers[key]
	bs2, ok2 := snap2.Buffers[key]
	if !ok1 || !ok2 {
		t.Fatalf("expected buffer key %q in both snapshots (ok1=%v, ok2=%v)", key, ok1, ok2)
	}
	if len(bs1.Entries) != len(bs2.Entries) {
		t.Errorf("entry count mismatch: eng1=%d eng2=%d", len(bs1.Entries), len(bs2.Entries))
	}
	for i := range bs1.Entries {
		if bs1.Entries[i].Value != bs2.Entries[i].Value {
			t.Errorf("entry[%d] value mismatch: %f vs %f", i, bs1.Entries[i].Value, bs2.Entries[i].Value)
		}
	}
}

// TestSaveAndLoadSnapshot_RoundTrip verifies save → load produces identical snapshot.
func TestSaveAndLoadSnapshot_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	original := evaluator.StateSnapshot{
		Version: 1,
		SavedAt: time.Now().UTC().Truncate(time.Millisecond),
		Buffers: map[string]evaluator.BufferSnapshot{
			"rule1:host=a": {
				Window:  5 * time.Minute,
				MaxSize: 100,
				Entries: []evaluator.EntrySnapshot{
					{Value: 42.0, At: time.Now().UTC().Truncate(time.Millisecond)},
				},
			},
		},
		Cooldowns: map[string]time.Time{
			"rule1:host=a": time.Now().Add(3 * time.Minute).UTC().Truncate(time.Millisecond),
		},
	}

	if err := evaluator.SaveSnapshot(path, original); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	loaded, err := evaluator.LoadSnapshot(path)
	if err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil snapshot")
	}

	if loaded.Version != original.Version {
		t.Errorf("version mismatch: %d vs %d", loaded.Version, original.Version)
	}
	if len(loaded.Buffers) != len(original.Buffers) {
		t.Errorf("buffers count mismatch: %d vs %d", len(loaded.Buffers), len(original.Buffers))
	}
	if len(loaded.Cooldowns) != len(original.Cooldowns) {
		t.Errorf("cooldowns count mismatch: %d vs %d", len(loaded.Cooldowns), len(original.Cooldowns))
	}

	bs := loaded.Buffers["rule1:host=a"]
	if len(bs.Entries) != 1 || bs.Entries[0].Value != 42.0 {
		t.Errorf("unexpected buffer entries: %+v", bs.Entries)
	}
}

// TestLoadSnapshot_FileNotExist verifies nil, nil is returned for missing files.
func TestLoadSnapshot_FileNotExist(t *testing.T) {
	snap, err := evaluator.LoadSnapshot("/nonexistent/path/state.json")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if snap != nil {
		t.Errorf("expected nil snapshot, got %+v", snap)
	}
}

// TestSaveSnapshot_AtomicWrite verifies the .tmp file is cleaned up after a save.
func TestSaveSnapshot_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	snap := evaluator.StateSnapshot{
		Version:   1,
		SavedAt:   time.Now().UTC(),
		Buffers:   map[string]evaluator.BufferSnapshot{},
		Cooldowns: map[string]time.Time{},
	}

	if err := evaluator.SaveSnapshot(path, snap); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// The .tmp file should not exist after the rename
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected .tmp file to not exist after atomic save")
	}

	// The real file should exist
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected state file to exist: %v", err)
	}
}

// TestStartFlusher_PeriodicFlush verifies the flusher writes the state file periodically.
func TestStartFlusher_PeriodicFlush(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	eng := makeWindowedEngine(t)
	stopFlusher := eng.StartFlusher(path, 20*time.Millisecond)

	// Poll for file existence up to 2 seconds (deterministic, not sleep-dependent)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	stopFlusher()

	// State file must exist after the flusher has fired at least once
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected state file to exist: %v", err)
	}

	// Should contain valid JSON
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read state file: %v", err)
	}
	var snap evaluator.StateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}
	if snap.Version != 1 {
		t.Errorf("expected version 1, got %d", snap.Version)
	}
}

// TestStartFlusher_FinalFlushOnStop verifies that stopping the flusher triggers a final flush.
func TestStartFlusher_FinalFlushOnStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	eng := makeCooldownEngine(t)
	now := time.Now()

	// Process an event so there is state to flush
	event := ingester.Event{
		Metric: "cpu_usage",
		Value:  97,
		Labels: map[string]string{"host": "web-01"},
		At:     now,
	}
	alerts := eng.Process(event, now)
	if len(alerts) == 0 {
		t.Fatal("expected alert to fire")
	}

	// Use a very long interval so the ticker never fires during this test
	stopFlusher := eng.StartFlusher(path, 1*time.Hour)
	stopFlusher() // triggers final flush and blocks until done

	// The state file should have been written by the final flush
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected state file after final flush: %v", err)
	}

	var snap evaluator.StateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}

	// Cooldown should have been captured
	if len(snap.Cooldowns) == 0 {
		t.Error("expected cooldowns in flushed state")
	}
	expectedKey := "cpu_spike:host=web-01"
	if _, ok := snap.Cooldowns[expectedKey]; !ok {
		t.Errorf("expected cooldown key %q in flushed state, got: %v", expectedKey, snap.Cooldowns)
	}
}

// keysOf is a helper to extract keys from a BufferSnapshot map for error messages.
func keysOf(m map[string]evaluator.BufferSnapshot) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
