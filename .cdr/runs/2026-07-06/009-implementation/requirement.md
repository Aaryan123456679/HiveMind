# Requirement — task-2a.4.4: Lock-free optimistic version-counter read

Subtask 4 of 5 under task-2a.4 (GitHub issue #9, B+Tree latch-crabbing concurrency).

## Acceptance criteria
- Reads (lookups) do not block writers and vice versa.
- A read that overlaps a concurrent structural mutation detects the conflict
  (via the node's version counter) and retries rather than returning
  corrupted/stale data.

## Test spec
`go test ./engine/btree/... -race -run TestOptimisticRead -timeout <N>`

## Impacted modules
- `engine/btree/lookup.go` (new function(s), existing free `Lookup` untouched)
- `engine/btree/lookup_test.go` (new `TestOptimisticRead`)

## Non-goals / explicit constraints
- Do NOT modify the existing Phase-1 free function `Lookup`'s signature or
  behavior — it is relied on throughout the engine and by existing tests.
- The new optimistic read path must NEVER call `NodeStore.Lock` or
  `NodeStore.TryLock`. Calling either would make readers block writers (or
  contend with them), defeating the entire point of this subtask.
- No retry cap on the whole-lookup restart loop (same convention as
  2a.4.2/2a.4.3's `crabInsert`/`crabDelete` restart-from-root loops).
- No latch/version-registry eviction (tracked open item, out of scope).
- Always use `-timeout` on every `go test` invocation.
