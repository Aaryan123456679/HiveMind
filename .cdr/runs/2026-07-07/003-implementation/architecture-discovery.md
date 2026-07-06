# Architecture Discovery — 2b.1.2

## `engine/split/trigger.go` (2b.1.1, already committed)

- `Trigger` is a pure, stateless value type (no mutable state after construction). `Detect(fileID,
  oldSizeBytes, newSizeBytes) (Signal, bool)` returns a `Signal{FileID, OldSizeBytes,
  NewSizeBytes, ThresholdBytes}` exactly once per genuine crossing (`CrossesThreshold`).
- `Trigger` explicitly does **not** remember which fileIDs it already signaled for — the
  doc comment states this is deliberate: the exactly-one-signal-per-crossing property is achieved
  structurally from the (old, new) size pair per call, not from memory inside `Trigger`, to avoid
  a second, independent "already signaled" flag drifting from the source of truth
  (`CatalogRecord.SizeBytes`).
- Consequence for 2b.1.2: the CAS guard is a **separate, independent** piece of state from
  `Trigger`. `Signal.FileID` (a `uint64`) is the natural key to guard on — the guard's public API
  should key on plain `uint64` fileID rather than importing `Signal` itself, so `engine/split`'s
  internal packages stay decoupled (guard doesn't need to know about Signal's other fields, and
  Trigger doesn't need to know about the guard). This subtask's issue text confirms the two are not
  required to be wired together by either 2b.1.1 or 2b.1.2 — 2b.1.3 does that wiring.
- Concurrency note from `Trigger`'s doc comment: callers evaluating concurrent appends to the
  *same* fileID are responsible for ensuring the (old, new) size pair passed to `Detect` reflects
  a serialized, non-torn view (today provided by `ContentStore.Append`'s per-fileID striped
  mutex). This CAS guard must NOT assume that invariant holds for its own callers — it must be
  correct even if multiple goroutines call `TryAcquire` for the same fileID truly concurrently,
  since that's exactly the scenario the issue describes (multiple independent callers deciding to
  attempt a split).

## `engine/btree/latch.go` (established per-key atomic-state-via-registry idiom)

- `NodeStore` owns `latches map[uint64]*nodeLatch` behind `latchesMu sync.Mutex`, with a
  `latchFor(nodeID uint64) *nodeLatch` lazily-creating accessor: lock the map mutex, look up or
  create-and-store the per-key struct, unlock, return the (stable, pointer-identity) `*nodeLatch`.
  All operations (`Lock`, `Unlock`, `TryLock`, `Version`) go through `latchFor` so the same
  `*nodeLatch` object is always returned for a given key.
- `nodeLatch` itself composes a `sync.Mutex` (writer serialization) with an `atomic.Uint64`
  (lock-free read versioning) — i.e., the registry holds the synchronization primitives, and the
  fine-grained state is a small dedicated struct, not a scalar in a map protected by one big lock.
- **No eviction**: `latch.go`'s registry never removes entries; this is a documented, deliberately
  deferred limitation (`.cdr/memory/pending.md`: "Node-latch follow-up... unbounded growth... 
  LRU-bounded reference-counted registry if it becomes an issue").
- **This new per-file registry has the same growth characteristic.** A `guardRegistry` (or
  similarly named) `map[uint64]*fileGuard` keyed by fileID, lazily created and never evicted, will
  grow unboundedly with the number of distinct fileIDs ever guarded, exactly like `NodeStore`'s
  `latches` map grows with distinct node IDs. Per the task instructions this is intentionally not
  solved now (matches precedent); a matching one-line note is added to `.cdr/memory/pending.md`.

## Other `engine/split` files

- Only `trigger.go`, `trigger_test.go`, `doc.go` exist. No existing guard/registry code to
  reconcile with. `doc.go` is a one-line package doc stub — no change needed there.

## Design decision for guard.go

- `FileGuard` (exported registry type, analogous to `NodeStore` but scoped to this package) holding
  `mu sync.Mutex` + `guards map[uint64]*fileSplitState`, lazily creating entries.
- `fileSplitState` holds a single `atomic.Bool` (`inProgress`). Go 1.26 has `atomic.Bool` with a
  `CompareAndSwap(old, new bool) bool` method — this is the real CAS primitive the issue demands
  (not a mutex+plain-bool).
- `TryAcquire(fileID uint64) bool`: lazily gets-or-creates the per-file state, then does exactly
  one `atomic.Bool.CompareAndSwap(false, true)`. Returns true iff this call was the one that
  flipped false->true (the winner); all concurrent/subsequent callers before release get false
  (losers, back off, no retry loop — matches issue's "back off without initiating duplicate
  split", not "spin until acquired").
- `Release(fileID uint64)`: transitions the flag back to false so a future threshold-crossing can
  win again. Design choice (documented in code): `Release` on a fileID that is not currently
  held is a documented no-op-if-already-false via plain `Store(false)` — simplest correct choice,
  matching `sync.Mutex.Unlock`-on-unheld-lock being caller error territory in this codebase is
  avoided by *not* panicking (consistent with the issue's phrasing "either a no-op or clearly
  erroring per your design" — chosen: no-op, since the guard has no notion of "owner" to
  attribute a wrongful-unlock error to, unlike `nodeLatch.mu` which is a real `sync.Mutex` that
  would itself panic on a double-unlock).
- No eviction of `guards` map entries (see growth-characteristic note above).
