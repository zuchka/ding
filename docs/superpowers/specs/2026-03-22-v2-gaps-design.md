# zuchka v2: Address Known v1 Gaps — Design Spec

**Date:** 2026-03-22
**Status:** Approved

---

## Problem

zuchka v1 shipped with four intentional limitations:

1. **No HTTP server timeouts** — `http.Server` has no `ReadTimeout`/`WriteTimeout`/`IdleTimeout`. A slow or hung client can hold a connection open indefinitely, threatening availability if the server is exposed publicly.
2. **No request body size limit on `/ingest`** — `io.ReadAll(r.Body)` with no bound means a sufficiently large POST can exhaust memory.
3. **No retry logic for failed webhooks** — delivery is fire-once; a transient downstream failure silently drops the alert.
4. **No persistent state** — ring buffers and cooldown timers live entirely in memory. Any restart clears all windowed aggregates and cooldown suppressions.

---

## Goals

- Address all four gaps in a single v2 release
- Remain backward-compatible (existing `ding.yaml` files need no changes)
- Add zero new external dependencies (stdlib only)
- Keep the implementation simple and auditable

---

## Non-Goals

- Persistent webhook retry queue (undelivered retries are lost on restart — in-memory only)
- Full event log or audit trail
- SQLite or any embedded database
- Distributed state sharing across multiple instances

---

## Design

### 1. Config Additions

All new fields are optional. Omitting them activates sensible defaults.

**`server` section** — new fields:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `read_timeout` | duration | `5s` | Max time to read the full request including body |
| `write_timeout` | duration | `10s` | Max time to write the full response |
| `idle_timeout` | duration | `60s` | Max idle time for keep-alive connections |
| `max_body_bytes` | int64 | `1048576` (1 MB) | Max `/ingest` request body size |

**New `persistence` section:**

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `state_file` | string | `""` (disabled) | Path to the JSON state snapshot file |
| `flush_interval` | duration | `30s` (only when `state_file` is set) | How often to flush state to disk |

`flush_interval` is only meaningful when `state_file` is non-empty. In `Validate()`: if `state_file == ""`, do not set `flush_interval` (leave zero — it is ignored); if `state_file != ""` and `flush_interval == 0`, set `flush_interval = 30s`.

**Per-notifier retry fields** (webhook only):

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_attempts` | int | `3` | Total delivery attempts (including the first). Retries = `max_attempts - 1`. |
| `initial_backoff` | duration | `1s` | Wait before the first retry (after the first attempt fails). |

`attempt` in `retryItem` is the number of failed attempts so far. `attempt=0` means the first delivery has not yet been tried. `attempt=1` means one failure; the next delivery waits `initialBackoff × 2^0 = initialBackoff`. Drop when `attempt >= maxAttempts`.

---

### 2. HTTP Timeouts

`cmd/ding/main.go` threads config values into `http.Server`:

```go
httpSrv := &http.Server{
    Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
    Handler:      srv.Handler(),
    ReadTimeout:  cfg.Server.ReadTimeout.Duration,
    WriteTimeout: cfg.Server.WriteTimeout.Duration,
    IdleTimeout:  cfg.Server.IdleTimeout.Duration,
}
```

No other files change for this feature.

---

### 3. Body Size Limit

`internal/server/handlers.go` acquires the config read-lock first (to obtain `cfg`), then wraps `r.Body` with `http.MaxBytesReader` before reading. The existing handler acquires `s.mu.RLock()` to read `cfg` partway through — the lock acquisition must be moved to before the `MaxBytesReader` call.

```go
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost { ... }

    // Acquire cfg BEFORE MaxBytesReader so MaxBodyBytes is in scope.
    s.mu.RLock()
    cfg := s.cfg
    eng := s.engine
    notifiers := s.notifiers
    s.mu.RUnlock()

    r.Body = http.MaxBytesReader(w, r.Body, cfg.Server.MaxBodyBytes)
    body, err := io.ReadAll(r.Body)
    if err != nil {
        if errors.As(err, new(*http.MaxBytesError)) {
            jsonError(w, "request body too large", http.StatusRequestEntityTooLarge)
            return
        }
        jsonError(w, "reading body: "+err.Error(), http.StatusBadRequest)
        return
    }
    // ... rest of handler uses cfg, eng, notifiers already captured above
}
```

`http.MaxBytesError` is available in Go 1.19+; the module targets Go 1.22. Add `"errors"` to imports.

---

### 4. Webhook Retry Queue

`internal/notifier/webhook.go` gains a background worker goroutine.

**Struct:**

```go
type retryItem struct {
    payload []byte
    rule    string
    attempt int       // number of failed delivery attempts so far (0 = never tried)
    nextAt  time.Time // earliest time to attempt delivery
}

// Queue capacity 256: at initialBackoff=1s with maxAttempts=3, a queue of 256 items drains
// in roughly 7s under full failure. Under sustained high alert volume with a failing webhook,
// the queue saturates quickly and drops are logged — this is the intended fail-fast behavior.
type WebhookNotifier struct {
    url            string
    client         *http.Client
    maxAttempts    int
    initialBackoff time.Duration
    queue          chan retryItem  // buffered, capacity 256
    stop           chan struct{}
    stopOnce       sync.Once
}
```

**`Send()`** — enqueues via non-blocking select with `nextAt = time.Now()`. If the queue is full, logs a drop warning and returns `nil`. Always returns `nil` (preserves existing `Notifier` interface contract).

**Worker goroutine** — started inside `NewWebhookNotifier`:

```go
func (n *WebhookNotifier) worker() {
    for {
        select {
        case <-n.stop:
            return  // abandon queued items (in-memory only; restart expectation)
        case item := <-n.queue:
            delay := time.Until(item.nextAt)
            if delay > 0 {
                // Use time.NewTimer (not time.Sleep) so stop is honored during backoff.
                t := time.NewTimer(delay)
                select {
                case <-t.C:
                case <-n.stop:
                    // Follow the standard Go timer drain idiom: if Stop() returns false,
                    // the timer already fired and its value is buffered on t.C — drain it.
                    if !t.Stop() {
                        <-t.C
                    }
                    return
                }
            }
            if err := n.deliver(item); err != nil {
                item.attempt++
                if item.attempt >= n.maxAttempts {
                    log.Printf("ding: webhook dropped after %d attempts for rule %q", n.maxAttempts, item.rule)
                    continue
                }
                // Backoff: initialBackoff * 2^(attempt-1), i.e. after attempt=1 wait initialBackoff×1,
                // after attempt=2 wait initialBackoff×2, etc.
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
```

**On `Stop()` — abandon vs. drain:** The worker abandons queued items when `stop` is closed. This is intentional: queued retries are in-memory only (explicitly a non-goal to persist them), and the shutdown path already calls `httpSrv.Shutdown()` before stopping notifiers, so no new `Send()` calls arrive after the drain. Items enqueued between `SwapEngine` completing and old-notifier `Stop()` being called are accepted into the old queue but are abandoned when that worker exits. This is an accepted best-effort trade-off consistent with the "delivery is best-effort" design.

**`deliver()` helper:**

```go
func (n *WebhookNotifier) deliver(item retryItem) error {
    resp, err := n.client.Post(n.url, "application/json", bytes.NewReader(item.payload))
    if err != nil {
        return err  // connection error → retry
    }
    defer resp.Body.Close()
    // Drain body to allow connection reuse, cap at 1KB to avoid unexpected large responses.
    io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
    if resp.StatusCode >= 500 {
        return fmt.Errorf("webhook returned %d", resp.StatusCode)  // retry
    }
    if resp.StatusCode >= 400 {
        // 4xx = payload problem; log and discard without retrying.
        log.Printf("ding: webhook %s returned %d for rule %q (not retrying)", n.url, resp.StatusCode, item.rule)
    }
    return nil
}
```

**`Stop()`** — uses `sync.Once` to close the stop channel exactly once, safe for multiple callers.

**Lifecycle integration:**
- Shutdown (`main.go`): after `httpSrv.Shutdown()`, iterate notifiers and call `Stop()` via structural type assertion `n.(interface{ Stop() })`
- Hot-reload (`server.go` `SwapEngine`): after atomically swapping to new notifiers, call `Stop()` on old notifiers outside the lock to prevent goroutine leaks

---

### 5. State Persistence

**New file:** `internal/evaluator/state.go` (same package — accesses private fields directly)

**Snapshot format (JSON, all timestamps in UTC):**

```json
{
  "version": 1,
  "saved_at": "2026-03-22T12:00:00Z",
  "buffers": {
    "high-cpu:host=web1": {
      "window_ns": 300000000000,
      "max_size": 10000,
      "entries": [{"value": 92.1, "at": "2026-03-22T11:58:00Z"}]
    }
  },
  "cooldowns": {
    "high-cpu:host=web1": "2026-03-22T12:05:00Z"
  }
}
```

All `time.Time` values written to the snapshot must use `.UTC()` to ensure timezone-independent round-trips and human-readable output.

**Key functions:**

| Function | Description |
|----------|-------------|
| `SnapshotEngine(e) StateSnapshot` | Captures state under separate locks (see locking below). Not fully atomic — acceptable for periodic persistence. |
| `RestoreEngine(e, snap, now)` | Applies snapshot. Called before serving traffic — no locking needed at startup. Drops entries older than `now - window`; drops expired cooldowns. |
| `LoadSnapshot(path) (*StateSnapshot, error)` | Returns `nil, nil` if file does not exist (first start). Errors on bad JSON or wrong version number. |
| `SaveSnapshot(path, snap) error` | Marshals to JSON, writes to `path+".tmp"`, renames atomically. All timestamps use UTC. |
| `(e *Engine) StartFlusher(path, interval) func()` | Returns `stopFlusher func()`. When `state_file == ""` this is never called; caller guards with `if cfg.Persistence.StateFile != ""`. |

**`SnapshotEngine` locking detail:**

`bufMu` protects the `map[string]*RingBuffer`, but each `RingBuffer` has its own `rb.mu` protecting its entries. Both must be acquired:

```
e.bufMu.Lock()
for key, rb := range e.buffers {
    rb.mu.Lock()
    // copy rb.entries
    rb.mu.Unlock()
}
e.bufMu.Unlock()

e.cooldown.mu.Lock()
// copy e.cooldown.expiry
e.cooldown.mu.Unlock()
```

`bufMu` and `cooldown.mu` are never held simultaneously. `rb.mu` is acquired and released inside the `bufMu` critical section — this is consistent with the existing lock ordering in `engine.go`.

**`RestoreEngine` locking:**

Called before the HTTP server begins serving traffic (per the startup sequence below). At that point no concurrent `Process()` calls exist, so locking is not strictly required. To be defensively correct and to support future call sites, `RestoreEngine` should still acquire `bufMu` and `cooldown.mu` in the same order as `SnapshotEngine`.

**`stopFlusher` nil-safety:**

`main.go` initializes `var stopFlusher func() = func() {}` (a no-op) before the persistence block, so the shutdown path can call `stopFlusher()` unconditionally without a nil check.

**Startup sequence (`main.go`):**
1. Load snapshot if `state_file` configured; log and ignore on error (start fresh)
2. Restore into engine (expired entries/cooldowns filtered)
3. Start flusher goroutine; capture `stopFlusher`
4. Begin serving HTTP traffic

**Shutdown sequence (`main.go`):**
1. `httpSrv.Shutdown()` — drain in-flight requests
2. Stop notifier goroutines (structural type assertion)
3. `stopFlusher()` — final state flush (blocks until complete)

**Hot-reload state transfer** — applies to both the SIGHUP handler in `main.go` and the `POST /reload` handler in `internal/server/handlers.go`. Both paths must follow this sequence:

1a. Call the current `stopFlusher()` — blocks until the final flush completes.
1b. Immediately set `stopFlusher = func() {}` before proceeding, so any early-return path below calls a safe no-op at shutdown.
2. Build new engine + notifiers from new config (`buildFromConfig`)
3. If `state_file` configured: `LoadSnapshot` + `RestoreEngine` into new engine
4. `SwapEngine` (atomically swaps engine/config/notifiers; stops old notifiers outside the lock)
5. `StartFlusher` on new engine; update `stopFlusher`

---

## Files Changed

| File | Nature of Change |
|------|-----------------|
| `internal/config/config.go` | Add fields + conditional defaults in `Validate()` |
| `internal/server/handlers.go` | Move lock acquisition before `MaxBytesReader`; add 413 error path; update `handleReload` for state-transfer sequence |
| `internal/server/server.go` | `SwapEngine` stops old notifiers after swap |
| `internal/notifier/webhook.go` | Retry queue + `deliver()` helper + worker goroutine + `Stop()` |
| `internal/evaluator/engine.go` | Add `StartFlusher()` method |
| `internal/evaluator/state.go` | **New file** — all persistence logic |
| `cmd/ding/main.go` | Wire timeouts; persistence startup/shutdown; hot-reload state-transfer |
| `ding.yaml.example` | Document all new config fields with commented-out examples |

---

## Testing Strategy

### Config (`internal/config/config_test.go`)
- `TestValidate_DefaultTimeouts` — zero timeouts → correct defaults applied
- `TestValidate_DefaultMaxBodyBytes` — zero → 1 MB
- `TestValidate_PersistenceDefaults` — `state_file` set, `flush_interval` omitted → 30s default
- `TestValidate_PersistenceNoStateFile` — `state_file` empty → `flush_interval` stays zero
- `TestValidate_WebhookRetryDefaults` — webhook notifier with no retry fields → `maxAttempts=3`, `initialBackoff=1s`

### Body size (`internal/server/server_test.go`)
- `TestIngest_BodyTooLarge` — body > limit → 413
- `TestIngest_BodyAtLimit` — body == limit → 200
- `TestIngest_BodyJustOverLimit` — body one byte over → 413

### Webhook retry (`internal/notifier/notifier_test.go`)
- `TestWebhookNotifier_RetriesOnServerError` — httptest returns 500 N times then 200; assert delivered
- `TestWebhookNotifier_NoRetryOn4xx` — server returns 400; assert exactly 1 attempt
- `TestWebhookNotifier_DropsAfterMaxAttempts` — server always 500, `maxAttempts=2`; assert 2 attempts then drop
- `TestWebhookNotifier_QueueFull_DropsSilently` — fill queue without worker; `Send` does not block, returns nil
- `TestWebhookNotifier_WorkerExitsOnStop` — start notifier, send alert, `Stop()`; goroutine exits without hang (queued items are abandoned, not drained)
- Use `initialBackoff=1ms` in all retry tests

### State persistence (`internal/evaluator/state_test.go` — new)
- `TestSnapshotEngine_Empty` — fresh engine → empty maps
- `TestSnapshotEngine_CapturesBuffers` — process events → entries in snapshot
- `TestSnapshotEngine_CapturesCooldowns` — trigger rule → cooldown in snapshot
- `TestRestoreEngine_RejectsExpiredEntries` — stale entries filtered out
- `TestRestoreEngine_RejectsExpiredCooldowns` — expired cooldowns dropped; rule fires immediately
- `TestRestoreEngine_PreservesActiveState` — roundtrip: process → snapshot → restore → same alerts
- `TestSaveAndLoadSnapshot_RoundTrip` — save to temp file, load back, assert equal
- `TestLoadSnapshot_FileNotExist` — nonexistent path → `nil, nil`
- `TestSaveSnapshot_AtomicWrite` — `.tmp` file absent after successful save
- `TestStartFlusher_PeriodicFlush` — short interval; state file created and valid JSON
- `TestStartFlusher_FinalFlushOnStop` — process events, `stopFlusher()`; file reflects latest state

### Integration
- `TestHotReload_StateTransfer` — process windowed events into engine; trigger reload; assert new engine produces same alerts as old engine would (verifies the flush→restore round-trip)

---

## Tricky Concerns

| Area | Concern | Resolution |
|------|---------|-----------|
| Config | Go map values not addressable | Reassign `cfg.Notifiers[name] = nc` after mutating local `nc` |
| Body limit | `cfg` not in scope at `MaxBytesReader` | Move `s.mu.RLock()` block to before the `MaxBytesReader` call |
| Webhook | `time.Sleep` misuse during backoff | Use `time.NewTimer` + `select` on stop channel during backoff sleep |
| Webhook | `time.NewTimer` stop-race leaves buffered value | Apply `if !t.Stop() { <-t.C }` before returning on the stop branch |
| Webhook | Items queued after `Stop()` on old notifier | Accepted best-effort loss; in-memory queue, not a non-goal breach |
| Webhook | Old goroutine leaks on hot-reload | `SwapEngine` calls `Stop()` on old notifiers after the swap |
| Webhook | `Stop()` called multiple times | `sync.Once` |
| Webhook | 4xx retries pointless | Only 5xx/connection errors trigger retry; 4xx logged and discarded |
| Webhook | `resp.Body` not drained → no connection reuse | `deliver()` drains up to 1 KB via `io.LimitReader` before close |
| Persistence | `bufMu` protects map, not buffer contents | Acquire per-buffer `rb.mu` inside the `bufMu` lock during snapshot |
| Persistence | Snapshot not fully atomic | Accepted — buffers and cooldowns captured under separate locks |
| Persistence | Corrupt state file on crash mid-write | Atomic rename of `.tmp` file prevents this |
| Persistence | Shutdown blocks on final flush | `stopFlusher()` blocks — called after HTTP drain |
| Persistence | `stopFlusher` nil panic if persistence disabled | Initialize to no-op: `var stopFlusher func() = func() {}` |
| Persistence | Hot-reload must cover both SIGHUP and `/reload` | Both code paths must implement the full 5-step state-transfer sequence |
| Persistence | Timestamps timezone-dependent | All snapshot timestamps written as `.UTC()` |
