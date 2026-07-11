# Architecture Discovery

## Existing retry-from-root loops (engine/btree)

1. `Tree.crabInsert` — `engine/btree/insert.go:598-610`. `for attempt := 0; ; attempt++`
   loop calling `crabInsertOnce`; on `err == errRestartFromRoot` increments
   `restartFromRootCount` and `continue`s forever; jittered backoff via
   `crabRetryBackoff(attempt)` for attempt > 0.
2. `Tree.crabDelete` — `engine/btree/delete.go:459-471`. Structurally identical
   loop calling `crabDeleteOnce`.
3. `Tree.Lookup` — `engine/btree/lookup.go:380-395`. Same shape but keyed on
   `errOptimisticRetry` instead of `errRestartFromRoot`. OUT OF SCOPE for
   4.5.1.2 (not in the issue's impacted-modules list; left untouched).

All three loops are currently genuinely unbounded (no cap, no max attempts).

## errRestartFromRoot semantics (insert.go:438-459)

Returned internally by `crabInsertOnce`/`findParentOnce`/`crabDeleteOnce` the
instant a hand-over-hand TryLock step would have had to block while already
holding a different node's latch. This is read-only up to that point (no
mutation has happened for the current attempt), so restarting is always
structurally safe — the retry-cap is NOT needed for correctness, only as a
defensive bound on wall-clock latency/liveness.

## Existing hook/test conventions

- `crabRetryHook func(nodeID uint64)` — package-level var, nil by default,
  invoked synchronously every time crabInsert/findParent (and, by extension,
  crabDeleteOnce) restarts after a TryLock miss. Settable in tests
  (`insert.go:461-468`).
- `crabRetryBackoff(attempt int)` — jittered, capped-at-2ms backoff
  (`insert.go:470-484`).
- `TestCrabbingConcurrentPropagateNoDeadlock` (insert_test.go:795-874) is the
  precedent test for deterministically forcing a TryLock miss: it directly
  `store.Lock()`s the node crabInsert's next hand-over-hand step will target,
  from a separate goroutine, so every TryLock attempt against that node
  fails until the goroutine releases it. It uses `crabRetryHook` purely to
  detect/count that the restart path was exercised (not to force the
  failure itself — the failure is forced by real lock contention).
- This SAME technique (permanently holding, never releasing, the contended
  node's latch) is sufficient to force TryLock to fail on every single
  attempt indefinitely, satisfying the issue's test-spec intent ("inject a
  hook forcing TryLock to always fail") without adding a brand-new hook to
  `latch.go` (which is out of scope / reserved for 4.5.1.3's eviction work).
  No new latch.go hook is introduced.
- `NodeStore.TryLock` (`latch.go:105-107`) is a thin wrapper over
  `sync.Mutex.TryLock`; nothing here needs modification.

## restartFromRootCount doc comment (latch.go:117-133)

Currently asserts: "none of these restart loops have, or should have, a
maximum-attempt cap — giving up would mean silently dropping a write or a
read, which this package never does." This sentence becomes stale for
crabInsert/crabDelete once the cap lands (Lookup is unaffected and remains
uncapped, consistent with the existing statement for reads). A minimal,
accuracy-only edit to this comment is required so the doc doesn't
contradict the code; this is not a behavioral change and does not touch
the latch eviction feature (4.5.1.3).
