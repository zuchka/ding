package evaluator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// StateSnapshot is the on-disk format for engine state.
type StateSnapshot struct {
	Version   int                       `json:"version"`
	SavedAt   time.Time                 `json:"saved_at"`
	Buffers   map[string]BufferSnapshot `json:"buffers"`
	Cooldowns map[string]time.Time      `json:"cooldowns"`
}

type BufferSnapshot struct {
	Window  time.Duration   `json:"window_ns"`
	MaxSize int             `json:"max_size"`
	Entries []EntrySnapshot `json:"entries"`
}

type EntrySnapshot struct {
	Value float64   `json:"value"`
	At    time.Time `json:"at"`
}

// SnapshotEngine captures a consistent point-in-time snapshot of engine state.
// acquires bufMu and each rb.mu for buffers, then cooldown.mu for cooldowns.
// Never holds both bufMu and cooldown.mu simultaneously.
// All timestamps are written as UTC.
func SnapshotEngine(e *Engine) StateSnapshot {
	snap := StateSnapshot{
		Version:   1,
		SavedAt:   time.Now().UTC(),
		Buffers:   make(map[string]BufferSnapshot),
		Cooldowns: make(map[string]time.Time),
	}

	e.bufMu.Lock()
	for key, rb := range e.buffers {
		rb.mu.Lock()
		entries := make([]EntrySnapshot, len(rb.entries))
		for i, ent := range rb.entries {
			entries[i] = EntrySnapshot{Value: ent.value, At: ent.at.UTC()}
		}
		snap.Buffers[key] = BufferSnapshot{
			Window:  rb.window,
			MaxSize: rb.maxSize,
			Entries: entries,
		}
		rb.mu.Unlock()
	}
	e.bufMu.Unlock()

	e.cooldown.mu.Lock()
	for k, v := range e.cooldown.expiry {
		snap.Cooldowns[k] = v.UTC()
	}
	e.cooldown.mu.Unlock()

	return snap
}

// RestoreEngine applies a snapshot to an engine before it starts serving traffic.
// Drops ring-buffer entries older than now-window and expired cooldowns.
// Acquires bufMu and cooldown.mu separately (defensive; RestoreEngine is typically
// called before serving begins, so locking is not strictly required at that point,
// but we acquire it for correctness at any future call site).
func RestoreEngine(e *Engine, snap StateSnapshot, now time.Time) {
	e.bufMu.Lock()
	for key, bs := range snap.Buffers {
		rb := NewRingBuffer(bs.Window, bs.MaxSize)
		cutoff := now.Add(-bs.Window)
		for _, ent := range bs.Entries {
			if ent.At.After(cutoff) {
				rb.entries = append(rb.entries, entry{value: ent.Value, at: ent.At})
			}
		}
		if len(rb.entries) > 0 {
			e.buffers[key] = rb
		}
	}
	e.bufMu.Unlock()

	e.cooldown.mu.Lock()
	for k, exp := range snap.Cooldowns {
		if exp.After(now) {
			e.cooldown.expiry[k] = exp
		}
	}
	e.cooldown.mu.Unlock()
}

// LoadSnapshot reads a snapshot from path.
// Returns nil, nil if the file does not exist (first start).
// Returns an error if the file exists but is corrupt or has an unsupported version.
func LoadSnapshot(path string) (*StateSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading state file: %w", err)
	}
	var snap StateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parsing state file: %w", err)
	}
	if snap.Version != 1 {
		return nil, fmt.Errorf("unsupported state version %d", snap.Version)
	}
	return &snap, nil
}

// SaveSnapshot writes a snapshot to path atomically (write to .tmp then rename).
// Prevents corrupt state files if the process is killed mid-write.
func SaveSnapshot(path string, snap StateSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing temp state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming state file: %w", err)
	}
	return nil
}
