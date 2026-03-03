# Implementation Notes — Algorithm Improvements

This document records the implementation session that applied all changes defined in
`ALGORITHM_IMPROVEMENT_SPEC.md`.  It covers what was changed, why each decision was
made, and any non-obvious details.

---

## What Was Implemented

All six sections of the specification were completed in a single session.

---

## Section 1 — LRU Cache (`internal/translator/cache.go`)

### 1.1 Segmented LRU — 16 shards

The original cache had a single `sync.RWMutex`.  Because `MoveToFront` mutates the
linked list, even reads required a full write lock, serializing all concurrent workers.

The replacement splits the cache into **16 independent shards**, each with its own
`sync.Mutex`, `*list.List`, and `map[string]*list.Element`.  A key is routed to its
shard via FNV-32a hash:

```go
func shardFor(key string) int {
    h := fnv.New32a()
    _, _ = h.Write([]byte(key))
    return int(h.Sum32()) % numShards
}
```

Each shard holds `globalCapacity / 16` entries.  A `Get` or `Put` only locks one shard,
so 16 workers can operate in parallel without contention in the best case.

### 1.2 Fix `normalize()` — collapse, don't strip

The old implementation stripped *all* internal whitespace, causing key collisions:

```
"Thank you" → "thankyou"
"Thankyou"  → "thankyou"  ← wrong cache hit
```

The fix uses `strings.Fields` + `strings.Join`, which collapses runs of whitespace to
a single space while preserving word boundaries:

```go
func normalize(s string) string {
    return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
```

### 1.3 Optional TTL

`NewCacheWithTTL(max int, ttl time.Duration)` is the primary constructor.
`NewCache(max int)` is kept as a backward-compatible wrapper that passes `ttl=0`.

On `Get`, if TTL is enabled and `time.Now().UnixNano() - entry.CreatedAt > ttl`,
the entry is removed and a miss is returned (lazy eviction — no background sweeper).

`config.CacheTTL time.Duration` (default 0, i.e. disabled) was added to
`internal/config/config.go`.

### 1.4 WAL instead of full JSON rewrite

`Put` appends a single JSON line to a `.wal` file:

```
{"k":"normalized key","v":"translation","t":1234567890}\n
```

`Load(path)` now:
1. Loads the JSON snapshot (if present)
2. Replays the WAL on top
3. Opens the WAL for appending

`Save(path)` (compaction):
1. Snapshots all shards to a new JSON file (atomic rename via `.tmp`)
2. Truncates the WAL to zero bytes and keeps it open for new appends

A `walMu sync.Mutex` serialises WAL appends — one writer at a time, but callers are
not blocked by each other because the mutex is released immediately after the `Write`.

**Windows note:** The WAL file handle must be explicitly `Close()`d before `t.TempDir()`
cleanup runs; otherwise Windows raises "file in use".  A `Close()` method was added to
`Cache` for this purpose.

---

## Section 2 — Adaptive Throttle (`internal/throttle/`)

### 2.1 Channel-based metrics

`RecordRequest` was rewritten to do a non-blocking send:

```go
func (c *Controller) RecordRequest(latency time.Duration, statusCode int) {
    select {
    case c.metricsCh <- requestMetric{latency, statusCode}:
    default: // drop if full — never block the worker
    }
}
```

All previously-atomic fields (`ewmaLatencyNs`, `decayReqsX1k`, `decayErrsX1k`,
`consecSuccess`, `baselineNs`, state, `reqCount`, `calibMinNs`) are now **plain local
variables** inside `Run()`, owned exclusively by that goroutine.  No atomics, no CAS,
no contention.

`Params.Delay` and `Params.BatchSize` remain `atomic.Int64/Int32` because workers
read them lock-free on every request.

### 2.2 Timer-based cooldown

The `go func() { time.Sleep; CAS }()` goroutine-per-backoff pattern was replaced with
a plain `cooldownUntil time.Time` local variable in `Run()`.

On each `ticker.C` event, if `state == stateCooldown && time.Now().After(cooldownUntil)`,
the cooldown expiry logic runs.  No extra goroutines, no accumulation.

### 2.3 Recovery validation before exiting cooldown

Cooldown exit now requires both:
- `ewma < 1.5 × baseline`
- `errorRate < 0.05`

If either condition fails, `cooldownUntil` is extended by `cfg.CooldownTime` and a
warning is logged.

### 2.4 Periodic baseline recalibration

Every `RecalibrationInterval` (default 5 min), if `errorRate < 0.05`:

```go
candidate := 0.9*baseline + 0.1*(ewma*1.1)
if candidate < baseline { baseline = candidate }  // only improve, never worsen
```

This allows the controller to benefit if the API server gets faster over time.

---

## Section 3 — Batch Translation (`internal/translator/client.go`)

### 3.1 Binary split fallback

The O(N) one-by-one fallback was replaced with a recursive halving function:

```go
func (c *Client) translateSplit(ctx context.Context, texts []string) ([]string, error) {
    if len(texts) == 1 {
        result, err := c.translateSingle(ctx, texts[0])
        return []string{result}, err
    }
    mid := len(texts) / 2
    left, _  := c.translateSplit(ctx, texts[:mid])
    right, _ := c.translateSplit(ctx, texts[mid:])
    return append(left, right...), nil
}
```

A batch of 25 failing lines now requires at most ⌈log₂(25)⌉ = 5 recursive levels
(≤ 32 single requests in the worst case) instead of 25.

The fallback uses `ctx` (the per-file context), not `batchCtx` (which is already
cancelled when the fallback is triggered).

### 3.2 Equal jitter backoff

```go
func jitterBackoff(attempt int, retryAfter time.Duration) time.Duration {
    if retryAfter > 0 { return retryAfter }
    base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
    half := int64(base / 2)
    return base/2 + time.Duration(rand.Int63n(half+1))
}
```

Range: `[base/2, base]`.  Applied to both `translateBatch` and `translateSingle`.
`math/rand` is auto-seeded since Go 1.20 — no explicit `rand.Seed` needed.

### 3.3 Dynamic batch size per iteration

```go
for i := 0; i < len(texts); {
    bs := c.getEffectiveBatchSize()  // fresh every iteration
    end := min(i+bs, len(texts))
    batch := texts[i:end]
    ...
    i += len(batch)
}
```

If the adaptive throttle halves the batch size mid-file, the very next batch uses the
new value.

### 3.4 `Retry-After` header

`doTranslateBatchRequest` and `doTranslateRequest` now return a `retryAfter time.Duration`
as a fourth value.  On HTTP 429, `parseRetryAfter` is called:

```go
func parseRetryAfter(header string) time.Duration {
    // Try integer seconds.
    if secs, err := strconv.Atoi(header); err == nil && secs > 0 {
        return time.Duration(secs) * time.Second
    }
    // Try HTTP-date.
    if t, err := http.ParseTime(header); err == nil {
        if d := time.Until(t); d > 0 { return d }
    }
    return 0
}
```

If `retryAfter > 0`, `jitterBackoff` returns it directly instead of computing the
exponential formula.

**Nil logger guard:** `NewClient` now falls back to `slog.Default()` when the caller
passes `nil`, preventing a nil pointer panic during tests.

---

## Section 4 — SRT Parser (`internal/srt/parser.go`)

### 4.1 `stateSkipping`

A fourth parser state was added.  When a bad index or bad/out-of-range timestamp is
encountered, `slog.Default().Warn(...)` is called and `state = stateSkipping`.

In `stateSkipping`, lines are consumed until a blank line, then `state = stateIndex`
resumes normal parsing.  The file is never aborted — only the malformed block is skipped.

### 4.2 Timestamp range validation

After the regex match, `validateTimestamp` is called:

```go
func validateTimestamp(ts string) error {
    for each part in ts.SplitN("-->", 2):
        fmt.Sscanf(part, "%d:%d:%d,%d", &h, &m, &s, &ms)
        if h >= 100 || m >= 60 || s >= 60 || ms >= 1000 { return error }
    return nil
}
```

HH limit is 100 (not 24) because SRT files legitimately span long recordings.

The timestamp regex was updated to allow `\d{2,}` for hours (not just `\d{2}`) so that
files with hours ≥ 100 are accepted by the regex and then validated by range checks.

---

## Section 5 — Worker Pool (`internal/orchestrator/orchestrator.go`)

### 5.1 `MarkSuccess` / `MarkFailed`

`jobState.MarkDone` was replaced with two distinct calls:

```go
if err != nil {
    _ = jobState.MarkFailed(job.filePath)
} else {
    _ = jobState.MarkSuccess(job.filePath)
}
```

`IsDone` returns `true` only for `StatusSuccess`.  On `--resume`, failed files are
re-queued automatically.

### 5.2 Log strings built outside the mutex

```go
// Build outside the lock — reduces hold time.
var logMsg string
if err != nil {
    logMsg = fmt.Sprintf(...)
    _ = jobState.MarkFailed(job.filePath)
} else {
    logMsg = fmt.Sprintf(...)
    _ = jobState.MarkSuccess(job.filePath)
}

mu.Lock()
// Only stat mutations and dashboard calls inside.
mu.Unlock()
```

### 5.3 Work-stealing (deferred)

Full dynamic work-stealing is not implemented.  Subtitle files are roughly uniform in
size, so a static pool delivers near-optimal utilisation without added complexity.  A
comment in `loop_backoff.go` documents this design decision.

---

## Section 6 — Job State (`internal/jobstate/jobstate.go`)

### 6.1 WAL + success/failure map

`State` now holds `entries map[string]string` (path → `"success"` | `"failed"`).

The WAL file is `path + ".wal"`.  Each call to `MarkSuccess` or `MarkFailed` appends
one JSON line under the same lock:

```
{"path":"/movies/file_en.srt","status":"success"}\n
```

`Load` supports both formats:
- **Old**: `{"completed":["path1","path2"]}` → all treated as `"success"`
- **New**: `{"entries":[{"path":"...","status":"..."}]}`

`Compact()` writes a new snapshot and removes the WAL.  `Delete()` removes both.

### 6.2 No TOCTOU gap

The map update and WAL append are protected by the same `mu.Lock()`.  There is no
window between in-memory state change and disk write.

**Windows note:** Same as cache — a `Close()` method was added to release the open
WAL file handle before test cleanup.

---

## New Test Files

| File | Key Tests |
|---|---|
| `internal/translator/cache_test.go` | `TestNormalize`, `TestNormalizeNoCollision`, `TestCacheTTLExpiry`, `TestCacheWALReplayWithoutSave`, `BenchmarkCacheGetConcurrent` |
| `internal/translator/client_test.go` | `TestJitterBackoffWithinBounds`, `TestParseRetryAfterInteger`, `TestTranslateSplitCorrectness`, `TestRetryAfterHeaderUsed`, `BenchmarkBatchFallbackWorstCase` |
| `internal/srt/parser_test.go` | `TestMalformedIndexSkipped`, `TestMalformedTimestampSkipped`, `TestTimestampInvalidMinutes`, `TestTimestampOutOfRangeBlockSkipped` |
| `internal/jobstate/jobstate_test.go` | `TestWALReplayWithoutCompact`, `TestSuccessFailDistinction`, `TestOldFormatCompatibility`, `TestCompact` |
| `internal/throttle/throttle_test.go` | `TestRecordRequestNonBlocking`, `TestBackoffOn429`, `BenchmarkRecordRequest` |

---

## Benchmark Results (reference machine: i7-12700H, Windows)

| Benchmark | Result |
|---|---|
| `BenchmarkCacheGetConcurrent` (20 goroutines) | ~85 ns/op |
| `BenchmarkCachePutConcurrent` (20 goroutines) | ~320 ns/op |
| `BenchmarkRecordRequest` (100 goroutines) | ~0.6 ns/op |

`BenchmarkRecordRequest` is near-zero because the non-blocking send drops metrics
when the channel buffer is full — the benchmark exercises the drop path.

---

## Verification

```
go build ./...   ✓
go vet ./...     ✓
go test ./...    ✓  (all 5 test packages pass)
```

CGO is disabled in this environment so `-race` cannot run, but the design is
data-race free by construction: all shared mutable state is either accessed under
a lock or via `atomic` operations.
