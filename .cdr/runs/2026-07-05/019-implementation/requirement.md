# Requirement — task-2a.4.2 (Latch-crabbing insert)

Issue #9 "Latch-crabbing B+Tree concurrency", subtask 2a.4.2.

## Acceptance criteria
- Concurrent inserts into disjoint subtrees proceed without blocking each other.
- Concurrent inserts into the same subtree remain correct.
- No writer ever holds more than a parent+child latch pair at once.

## Test spec
`go test ./engine/btree/... -race -run TestCrabbingInsert`: concurrent inserts
across disjoint and overlapping subtrees; assert final tree contains all
inserted keys with correct structure.

## Impacted modules
`engine/btree/insert.go`, `engine/btree/insert_test.go`.

## Prerequisite (2a.4.1, commit 5cc69e3, verified PASS_WITH_COMMENTS)
- `NodeStore` owns a lazily-populated `map[uint64]*nodeLatch` registry
  (`latchesMu`-guarded), keyed by node ID.
- `nodeLatch.mu` is a plain `sync.Mutex`, writer-only.
- `nodeLatch.version` is an `atomic.Uint64`, bumped by exactly 1 on every
  successful `WriteNode`.
- Public API: `NodeStore.Lock(nodeID)` / `Unlock(nodeID)` / `Version(nodeID)`.
- `WriteNode` does NOT take the latch itself; callers must Lock before /
  Unlock after their `WriteNode` call(s). Zero call sites used this
  convention as of 2a.4.1 — this subtask is the first to wire it up.

## New ground this subtask must cover
Insert's existing signature (`Insert(store, alloc, rootNodeID, path, fileID)
(newRootNodeID, err)`) requires the CALLER to track the root ID across
calls. This is fundamentally incompatible with concurrent multi-writer
access: two goroutines racing on the same tree cannot each safely pass in
their own stale `rootNodeID` and expect the other's structural changes
(especially root splits) to be visible/coordinated. This subtask must
introduce an in-memory, shared, mutex-protected root pointer as new
tree-level state (not covered by 2a.4.1's per-node latch registry, which
only protects node CONTENT, not "which node ID is currently the root").
`persist.go`'s `SaveRoot`/`LoadRoot` remain the durability mechanism for
across-process-restart root recovery and are orthogonal to this in-memory
concurrency concern; they are not modified by this subtask.
