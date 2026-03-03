# Algorithm Improvement Specification

This document defines required architectural and algorithmic improvements for the current codebase.

## Goal

The goal is to:

- Preserve correctness and crash-safety guarantees.
- Improve concurrency scalability.
- Reduce I/O amplification.
- Eliminate pathological behavior under load.
- Maintain backward compatibility of public APIs.

All changes must be production-grade and benchmark-aware.

---

## 1. LRU Cache Improvements

### 1.1 Replace Global Write Lock on Get

**Problem**
`Get()` acquires a full `mu.Lock()` because `MoveToFront()` mutates the linked list. This serializes all concurrent readers.

**Required Change**
Implement one of the following:

**Option A — Probabilistic Promotion**

- Use `RLock()` for `Get`.
- Promote to front only on a probability threshold (e.g., 1/8 reads).
- Use an atomic counter or fast random for sampling.
- If promotion occurs, upgrade to a full lock.

_This reduces lock contention drastically under high concurrency._

**Option B — Segmented LRU (Preferred)**
Split the cache into N shards (e.g., 16 shards): `hash(key) % shardCount`

Each shard has its:

- Own mutex
- Own linked list
- Own capacity `(global capacity / shardCount)`

_This eliminates global serialization._

### 1.2 Fix Key Normalization (Avoid Collisions)

**Problem**
All whitespace is stripped:

- `"Thank you"` → `"thankyou"`
- `"Thankyou"` → collision

**Required Change**
Replace whitespace removal with whitespace normalization:

- Trim leading/trailing whitespace.
- Convert multiple internal whitespace runs to a single space.
- Preserve single space between words.
- Lowercase result.

**Example:**
`"Thank you\t"` → `"thank you"`

_(Do NOT remove internal spaces entirely)._

### 1.3 Add Optional TTL Support

Add optional TTL per entry:

- Store `createdAt` timestamp.
- On `Get`, treat expired entries as a miss.
- Expired entries are removed lazily.
- TTL is configurable via config.

_TTL must be optional and disabled by default._

### 1.4 Eliminate Full JSON Rewrite on Every Save

**Problem**
The entire cache is serialized on every flush.

**Required Change**
Switch to append-only WAL (write-ahead log):

**On Put:**

- Append `{key, value}` as a single JSON line.

**On startup:**

- Replay WAL.
- Reconstruct LRU in MRU order.
- Compact into a single snapshot file once.

Periodic compaction is allowed. Must remain crash-safe using atomic rename.

---

## 2. Adaptive Throttle Improvements

### 2.1 Remove CAS Spin Loops

**Problem**
EWMA update uses CAS spin under heavy contention.

**Required Change**
Replace atomic EWMA updates with:

- Single owner goroutine.
- Workers send metrics over a buffered channel.
- Controller goroutine updates EWMA and counters.

Hot path must be non-blocking: Channel send must use non-blocking `select` with a drop fallback.

### 2.2 Periodic Baseline Recalibration

Baseline must not be static.

**Add:**

- Recalibration window every X minutes.
- Only recalibrate if the system is stable (low error rate).
- Smooth transition (do not instantly override).

### 2.3 Replace Goroutine-per-Backoff

**Problem**
Each backoff spawns:

```go
go func() { sleep; CAS() }()
```

**Required Change**
Replace with:

- Single timer in controller loop.
- Cooldown state tracked internally.
- No goroutine spawn per backoff event.

### 2.4 Add Recovery Validation

Cooldown exit must check:

- `EWMA < threshold`
- Error rate below threshold

If not satisfied:

- Extend cooldown.
- Do not flip to `Normal`.

---

## 3. Batch Translation Improvements

### 3.1 Replace O(N) Fallback with Binary Split

**Current behavior:**
Batch fail → retry all N individually

**Required Change:**
Recursive split:

```go
func retryBatch(batch):
    if len(batch) == 1:
        retry single
    else:
        split into left/right
        retry each half
```

_This isolates the failing line in O(log N) calls._

### 3.2 Add Exponential Backoff with Jitter

Replace deterministic: `2^attempt seconds`
With: `base * 2^attempt ± random(0–50%)`

Use full jitter or equal jitter strategy. Must prevent thundering herd across workers.

### 3.3 Dynamic Batch Size Per Iteration

**Instead of:**

```go
bs := getEffectiveBatchSize() // once per file
```

**Move inside loop:**

```go
for {
    bs := getEffectiveBatchSize()
    // ...
}
```

Throttle adjustments must affect subsequent batches immediately.

### 3.4 Support Retry-After Header

When HTTP `429`:

- Parse `Retry-After`.
- If valid, use it.
- Else, fallback to exponential backoff.

---

## 4. SRT Parser Hardening

### 4.1 Do Not Abort Entire File on Single Malformed Block

**Replace hard error:**

```go
return error
```

**With:**

- Log warning.
- Skip malformed block.
- Continue scanning until next blank line.

_Parser must be resilient to partially corrupted files._

### 4.2 Validate Timestamp Ranges

Ensure:

- `HH < 24`
- `MM < 60`
- `SS < 60`
- `MS < 1000`

Reject block if invalid.

---

## 5. Worker Pool Improvements

### 5.1 Separate Success and Failure State

Job state must store:
`map[path]status`
Where status ∈ `{ success, failed }`

`--resume` must:

- Skip success.
- Retry failed.

### 5.2 Reduce Lock Hold Time in Logging

Build log strings outside mutex.

### 5.3 Optional: Dynamic Work Stealing

**Stretch goal:**
Replace static worker pool with:

- Work queue + dynamic scaling
- Or adaptive worker count

_(Not mandatory but encouraged)._

---

## 6. Job State Persistence Improvements

### 6.1 Replace Full Rewrite with Append-Only Log

Instead of rewriting entire JSON file:

**On completion:**
Append: `{"path":"...","status":"success"}`

**On startup:**

- Replay log.
- Build in-memory map.

**Optional:**
Compact log to snapshot periodically.

### 6.2 Remove TOCTOU Snapshot Gap

Snapshot must be created under lock or using immutable copy mechanism.
No window where in-memory state differs from persisted snapshot.

---

## 7. Non-Functional Requirements

- Maintain full backward compatibility of CLI.
- Preserve crash-safety semantics (atomic rename required).
- No global mutex on hot path.
- Add benchmarks for:
  - Cache `Get` under 100 concurrent goroutines
  - Throttle metric update throughput
  - Batch fallback worst-case behavior
- Add unit tests for:
  - Whitespace normalization
  - Binary split retry correctness
  - Retry-After handling
  - TTL expiry
  - Malformed SRT recovery

---

## Deliverables

Claude must:

- Refactor code to implement all required improvements.
- Preserve public interfaces unless absolutely necessary.
- Add unit tests and benchmarks.
- Document all architectural changes in comments.
- Ensure `go vet` and `golangci-lint` pass cleanly.

---

_End of specification._
