# task-2a.4.1 — Per-node latch and version counter

## Summary
First of 5 subtasks under task-2a.4 (B-tree latch-crabbing concurrency, GitHub issue #9).
`engine/btree`'s `NodeStore` previously had zero synchronization: nodes are decoded fresh
from disk into value structs on every `ReadNode`/`WriteNode` call, with no in-memory
identity to attach per-node state to. This subtask adds a `NodeStore`-owned registry,
keyed by node ID, that gives each node a write latch and a version counter — the
foundational primitive that latch-crabbing insert/delete (2a.4.2/2a.4.3) and optimistic
lock-free reads (2a.4.4) build on directly.

## Features
- `NodeStore.Lock(nodeID)` / `NodeStore.Unlock(nodeID)`: acquire/release a node's
  write-only latch (plain `sync.Mutex`, not `RWMutex` — readers never take it, by design,
  so a future reader never blocks on a writer or another reader).
- `NodeStore.Version(nodeID) uint64`: non-blocking atomic read of a node's version counter.
- Single-increment-after-mutation versioning scheme (not a seqlock odd/even pair):
  `WriteNode`, the sole existing choke point for structural node mutations, bumps the
  target node's version by exactly one immediately after each successful write.
  `WriteNode` deliberately does not acquire the latch itself, since a non-reentrant mutex
  would deadlock a crabbing caller already holding it across its own `WriteNode` calls.
- Lazily-populated `map[uint64]*nodeLatch` registry guarded by a single mutex; the
  check-then-insert sequence is one critical section, so there is no lazy-population race.
- `TestNodeLatchFields` exercises single, sequential, isolated, and 50-goroutine
  concurrent mutation counts under `-race`.
- Existing `insert.go`/`delete.go` call sites are intentionally left unwired — that is
  explicitly 2a.4.2/2a.4.3's job, not this subtask's.

## Impact
Foundational, not user-facing. No behavior change for today's single-threaded
insert/delete paths. Establishes the exact `Lock`/`Unlock`/`Version` API and locking
convention that subtasks 2a.4.2 through 2a.4.5 depend on; any deviation from the
documented convention (callers must hold a node's latch across the `WriteNode` call(s)
that mutate it) will need to be caught during those subtasks' review.

Two non-blocking findings from verification are tracked as Phase 2a follow-ups rather
than blockers (see `.cdr/memory/pending.md`):
1. The latch registry has no eviction — undisclosed as a known limitation in `latch.go`,
   though precedented elsewhere in the codebase.
2. `node.go`'s old doc comments describing "actual CAS/atomic version-bump logic" are
   now stale, since a separate in-memory counter was built instead of reusing the
   on-disk `Version` field.

## Verification
- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `2026-07-05-017-verification`
- Confirmed the lazy-population race flagged as a risk is NOT present: `latchFor`'s
  check-then-set sequence is one critical section under a single mutex.
- Confirmed `WriteNode` is genuinely the sole node-content mutation choke point:
  `SaveRoot`/`NodeAllocator` write to disjoint sidecar files, not node content.
- Confirmed the 50-goroutine concurrent test is genuine and exact.

## Release Notes
Internal/foundational change; no user-facing behavior change. Adds a per-node
write-latch and version-counter primitive to `engine/btree` in preparation for
concurrent (latch-crabbing) insert/delete and lock-free optimistic reads.
