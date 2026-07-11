# Architecture discovery

## Current locking primitives (`engine/graph/edgelog.go`, HEAD c1558cf)

`EdgeLog` has exactly one lock today: `mu sync.RWMutex`, guarding only the
`writers map[uint64]*wal.Writer` cache (who currently has a `wal.Writer` open
for a given `sourceFileID`). It is taken by:
- `getOrOpenWriter` (RLock to check the cache, Lock to create-and-cache a new
  `wal.Writer` on miss) — released before the caller does any actual I/O.
- `TruncateNode` (Lock held for its ENTIRE body: closes/evicts the cached
  writer, lists segment files fresh from disk, writes the segment floor,
  removes every segment file currently present).
- `Close` (Lock, closes every writer).

`ReadNodeAfter` takes **no lock at all** — it lists `wal-<N>.log` files
directly via `os.ReadDir` and reads them via `wal.ReadSegment`, entirely
independent of `l.mu`.

`AppendEdge` takes `l.mu` only transiently, inside `getOrOpenWriter`, to
fetch/create the per-node `*wal.Writer`. The actual `w.Append(buf)` call that
follows is guarded only by the `wal.Writer`'s own internal `sync.Mutex`
(`engine/wal/writer.go`'s `Writer.mu`), which `l.mu` has no relationship to.

## The exact race window

`Compact` (`engine/graph/compact.go`, `Compact` function, current lines
~437-463):
1. For each node id: `logEdges, maxSeg, err := log.ReadNodeAfter(id, afterSeg)`
   — lists+reads segment files **as of this instant**, with no lock held.
2. Merges `logEdges` into in-memory `adjacency` (fast, pure CPU).
3. Records `newState[id] = uint64(maxSeg)` and adds `id` to
   `compactedNodeIDs`.
4. ... loop continues for every other node ...
5. `BuildCSR` + `WriteCSR(graphPath, newGraph)` — writes the whole snapshot
   to disk (potentially the slowest step, proportional to total graph size,
   NOT just this one node).
6. `saveCompactState(graphPath, newState)`.
7. Only now, for each `id` in `compactedNodeIDs`: `log.TruncateNode(id)` —
   this **re-lists segment files from disk at truncate time** (`segments,
   err := listWALSegmentsNumbered(dir)` inside `TruncateNode`) and removes
   **every segment file it finds**, not just the ones `ReadNodeAfter` saw in
   step 1.

Between step 1 (this node's `ReadNodeAfter`) and step 7 (this node's
`TruncateNode`), arbitrarily much time passes — at minimum the rest of the
node loop plus a full `WriteCSR` of the entire graph. If a concurrent
`AppendEdge(id, ...)` call lands anywhere in that window:
- `wal.Writer.Append` (`engine/wal/writer.go`) only rotates to a **new**
  segment file once the **current** segment would exceed
  `defaultMaxSegmentBytes` (4 MiB, `edge_append.go`/`edgelog.go`). A single
  `CSREdge` is tiny, so in practice the freshly-appended record lands
  **inside the very same segment file** `ReadNodeAfter` already fully read
  (not a new segment number) — `w.Append` just appends more bytes to the
  file that is still the writer's "current" segment.
- `TruncateNode`'s later re-listing sees that same segment file (its number
  is `<= maxSeg`, since it didn't rotate) and deletes it outright via
  `os.Remove`, discarding the just-appended record along with the
  already-merged ones. The just-appended edge was NEVER read by
  `ReadNodeAfter` (it didn't exist yet), was NEVER merged into the
  `graph.dat` `WriteCSR` just performed, and is now permanently gone from
  disk — exactly issue #49's described failure mode.
- Because `defaultMaxSegmentBytes` is 4 MiB and test edges are a few dozen
  bytes, a fix that ONLY threads "the exact `maxSeg` number `ReadNodeAfter`
  saw" into `TruncateNode` (naively: "only delete segments numbered `<=
  maxSeg`") is **insufficient on its own**: the concurrently-appended record
  can land inside the *same* segment file whose number is `<= maxSeg` (no
  new segment was created), so a purely segment-count-based filter would
  still delete that exact file and lose the new record. Splitting a segment
  file's own bytes (keep the tail written after the read, discard only the
  head already merged) is not a primitive `engine/wal` exposes, and building
  one is a much larger change than this subtask's single-commit scope
  justifies.

## Existing hook convention for deterministic concurrency tests

This codebase has an established, repeated idiom (not this run's invention)
for deterministically forcing a goroutine into a specific window for race
tests, used identically in three other places:
- `engine/btree/insert.go`'s `crabRetryHook func(nodeID uint64)` /
  `engine/btree/lookup.go`'s `optimisticReadHook`/`optimisticRetryHook`.
- `engine/catalog/content.go`'s `createWithHook` (public `Create` calls
  `createWithHook(rec, data, nil)`).
- `engine/mvcc/read.go`/`write.go`'s `newSnapshotWithHook`/
  `commitVersionWithHook`.
- `engine/split/execute.go`'s `atomicCommitHook func(stage string) error`,
  invoked via `runAtomicCommitHook(stage)` at named stages, with the public
  `ExecuteSplitAtomic` never itself checking the hook (nil in production).

All are unexported package-level `var ... func(...)`, nil (no-op) in
production, set directly by same-package `_test.go` files (both
`compact_test.go` and `edgelog_test.go` are `package graph`, not
`package graph_test`), invoked synchronously at one well-documented point.
This subtask's fix follows the exact same shape rather than inventing a new
one.

## Approach chosen: (a) lock-scope extension, per-node — with justification

Per the acceptance criteria's own two options and the prompt's invitation to
pick whichever fits this codebase's precedent (referencing #38-42's
btree/mvcc concurrency fixes): those precedents (`crabRetryHook`,
`optimisticReadHook`, MVCC's epoch/version locking) are all instances of
"hold the narrowest correct lock/version-check for exactly as long as needed
to make a specific window atomic," not instances of "capture a scalar and
re-validate it later." Given the analysis above — that a purely
segment-count/maxSeg-based "truncate exactly what I read" filter cannot
close the race on its own (the concurrent append can land inside, not just
after, the already-read segment) — a byte-precise segment-splitting version
of option (b) would be substantially more invasive (would need to teach
`engine/wal` how to split a live segment file's bytes and hand the surviving
tail to the still-open `wal.Writer`, or force a mid-flight rotation
mid-Append) than this subtask's single-commit scope should require.

Option (a), correctly scoped, is both simpler and strictly stronger: add one
new lock primitive to `EdgeLog` — a lazily-created **per-node**
`*sync.Mutex` (`nodeLocks map[uint64]*sync.Mutex`, guarded by its own
`nodeLocksMu`, exposed via `EdgeLog.LockNode(id) (unlock func())`) — and:
- `AppendEdge` holds that node's lock for its entire body (get-or-open
  writer through the durable `Append` call), not just the writer-map lookup.
- `Compact` acquires the SAME node lock immediately before calling
  `ReadNodeAfter(id, ...)` for that node, and does not release it until
  AFTER that node's `TruncateNode(id)` call has returned (i.e. held across
  the intervening `WriteCSR`/`saveCompactState` calls for nodes that end up
  in `compactedNodeIDs`; released immediately, right after the read, for
  nodes with `maxSeg < 0` — nothing to truncate, nothing to protect).

This makes "AppendEdge for node X" and "Compact's read-then-truncate of node
X" strictly mutually exclusive for the SAME node, closing the race
completely regardless of whether the concurrent append lands in an existing
segment or a new one, at the cost of blocking `AppendEdge` calls for nodes
actively being compacted for the duration of one `WriteCSR` call (bounded,
proportional to graph size, not per-node — an accepted, disclosed tradeoff;
`AppendEdge` calls to OTHER, not-currently-being-compacted nodes are
completely unaffected, since the lock is per-node, not global). No deadlock
risk: lock acquisition order is always `nodeLock(id)` before `l.mu` (via
`getOrOpenWriter`/`TruncateNode`'s existing internal `l.mu` use), consistent
across both `AppendEdge` and `Compact`'s call paths, and there is only ever
one `Compact` goroutine assumed at a time (matching this file's existing
single-compactor assumption — `Compact`/`saveCompactState`'s sidecar
mechanism already assumes non-concurrent `Compact` calls).

`TruncateNode` itself is left otherwise unchanged (still takes only `l.mu`
internally) — it is not re-entered while `Compact` already holds the node
lock, since `nodeLock` and `l.mu` are different mutexes, so no self-deadlock.
Direct standalone test callers of `TruncateNode`/`ReadNodeAfter` outside
`Compact` (several exist in `compact_test.go`, e.g. `TestTruncateNode`)
are unaffected — this fix does not change their signature or behavior when
called without concurrent `AppendEdge` traffic.

## New test-only hook

Add `var compactNodeLockedHook func(id uint64)` in `compact.go` (nil in
production), invoked synchronously once per node, immediately after
`Compact` has acquired that node's lock and `ReadNodeAfter` has returned
(lock still held). `TestCompactConcurrentAppendNotLost` sets this to kick
off a concurrent `AppendEdge` goroutine for the SAME node id (which will
correctly block on the node lock until `Compact` finishes truncating and
unlocks), then asserts: (1) the first `Compact` call's own written
`graph.dat` does NOT yet contain the concurrently-appended edge's weight
(the append hadn't happened yet from `ReadNodeAfter`'s point of view) —
i.e. it isn't racily included either, only the pre-existing edge is;
(2) after the concurrent `AppendEdge` call unblocks and returns
successfully, a SECOND `Compact` call picks it up and merges it correctly
(summed weight for `EdgeEntityCooccur`); (3) run under `-race`, confirming
no data race between the node-lock-protected paths.
