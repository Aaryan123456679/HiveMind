# Architecture discovery

## Files read (in full or targeted sections)

- `engine/btree/latch.go` (whole file) -- `nodeLatch`, `latchFor`,
  `getOrCreateLocked`, `acquireLatch`, `peekLatch`, `releaseLatch`, `Lock`,
  `Unlock`, `TryLock`, `Version`.
- `engine/btree/lookup.go:175-240` -- `errOptimisticRetry`,
  `optimisticReadHook`/`optimisticRetryHook` package-level hook vars and their
  doc comments, `readNodeOptimistic`, `lookupOnce`.
- `engine/btree/insert.go:455-470,670-1040` -- `crabRetryHook` var and its
  call sites, confirming the established "package-level func var, nil in
  production, invoked synchronously at one documented point, swapped by tests
  via save-prev/defer-restore" idiom used consistently across this package's
  three existing test hooks (`crabRetryHook`, `optimisticReadHook`,
  `optimisticRetryHook`).
- `engine/btree/latch_test.go` (whole file) -- existing 5 tests, in
  particular `TestNodeLatchNoDoubleLockAcrossEviction` (the existing,
  stress/probabilistic double-lock regression test the verifier correctly
  identified as insufficient) and `newLatchTestStore`/`encodeTestLeaf` test
  helpers used by all of them.
- `.cdr/runs/2026-07-11/047-verification/verification.json` and
  `handoff.json` -- the CHANGES_REQUESTED verdict and blocking finding.

## Key findings

1. This package already has an established, consistent pattern for exactly
   this problem (deterministic forcing of a race window via a nil-able
   package-level func var invoked at one specific point, swapped in/out by
   tests with save/restore). `optimisticReadHook` is the closest precedent:
   it is invoked inside `readNodeOptimistic`, between the content read and the
   confirming version re-read, to let a test force a real concurrent write to
   land in that exact window instead of relying on timing.

2. `Unlock`'s current body is exactly two operations in sequence:
   ```go
   l.mu.Unlock()
   s.releaseLatch(nodeID, l)
   ```
   `releaseLatch` decrements `refs` and, only if `refs` reaches 0 AND
   `l.version.Load() == 0`, deletes nodeID's registry entry. Because `Lock`'s
   own `acquireLatch` call already incremented `refs` for the lifetime of the
   "checked out" window, and nothing else can decrement it back to 0 while
   this call's own reference is still outstanding, `releaseLatch`'s eviction
   check is the ONLY place that can make nodeID's entry disappear as a direct
   consequence of this specific `Unlock` call.

3. Whether reversing the two lines is actually exploitable is entirely
   deterministic once you look at what `acquireLatch`/`getOrCreateLocked` do:
   if `releaseLatch` runs first and evicts the entry (refs 1->0, version==0,
   which is exactly the common case for latches on freshly-crabbed,
   never-yet-written nodes -- i.e. NOT a corner case), a concurrent
   `Lock(nodeID)` racing in between the two (now-reversed) lines will call
   `acquireLatch` -> `getOrCreateLocked`, find no entry, and allocate a
   BRAND NEW `nodeLatch` with a fresh, unlocked `mu`. That concurrent caller's
   `l.mu.Lock()` succeeds immediately on the new object, while the ORIGINAL
   object's `mu` is still actually locked (the reversed code hasn't reached
   `l.mu.Unlock()` yet). Two different goroutines, two different mutex
   objects, both "believing" they hold nodeID's latch: a real, reachable
   double-lock, not a hypothetical one. This confirms the verifier's read:
   the claim is TRUE and REAL, just untested.

4. Because this package is white-box-testable (test file is in `package
   btree`), the deterministic reproduction does not require literally
   shipping the buggy reversed code anywhere -- a hook placed textually
   between the two operations inside the real `Unlock` body lets a test pause
   exactly at the boundary and inspect state on both sides, for whichever
   ordering is currently compiled in. This means the SAME hook, at the SAME
   source location, distinguishes both orderings: swapping the two statements
   (as a local, uncommitted mutation-test step) leaves the hook still
   textually between them, so the test exercises "whatever ran first" vs.
   "whatever runs second" symmetrically without needing two code paths.

5. Chose option (a) (real test) over (b) (soften comment): the investigation
   in point 3 shows the hazard is genuine and reachable (not merely a
   theoretical concern already fully mitigated by something else), so
   softening the comment to "not really load-bearing" would be factually
   wrong. A deterministic test is both correct and stronger.
