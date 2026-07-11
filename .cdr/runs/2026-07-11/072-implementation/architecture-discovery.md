# Architecture Discovery

## Sources read
- `engine/split/guard.go` (full, current state at HEAD `b3aa8e2`) -- `fileSplitState`, `FileGuard`,
  `getOrCreateLocked`, `acquireGuard`, `releaseGuard`, `TryAcquire`, `Release`, `InProgress`,
  `guardRegistrySize`.
- `engine/btree/latch.go` (full, post-`94c24e6`) -- `nodeLatch`, `NodeStore.latches`,
  `acquireLatch`/`releaseLatch`, `Lock`/`Unlock`, `unlockOrderHook`,
  `TestNodeLatchUnlockOrderingPreventsDoubleLock` (referenced by doc comment; test file itself not
  needed for this decision since the mechanism -- a nil-in-production func var invoked at the exact
  step boundary -- is fully described in the doc comments).
- `.cdr/runs/2026-07-11/070-verification/verification.json` in full.

## Key structural difference between the two eviction gates

`NodeStore.latches`' gate: `refs == 0 && l.version.Load() == 0`.
- `version` is monotonically-increasing history, completely independent of the mutex (`l.mu`) that
  `Lock`/`Unlock` protects. `Unlock` unconditionally unlocks `l.mu` first, THEN calls
  `releaseLatch`. Because unlocking the mutex has no bearing on whether the `version==0` gate
  passes, eviction can become possible (`refs` hits 0 while `version` happens to still be 0)
  *immediately* after the mutex is unlocked but before `releaseLatch` runs -- opening a real window
  where a racing `Lock(nodeID)` can create a **fresh** `nodeLatch` object (evicted-and-replaced)
  whose **new, unlocked** mutex it immediately acquires, while the **original** mutex object is
  still, in fact, locked (not yet unlocked) by... no wait, actually original is unlocked already at
  that point; the actual race btree's test proves is the reverse ordering (releaseLatch before
  Unlock's mu.Unlock) -- reversing exposes two goroutines each holding a *different* nodeLatch
  object's mutex for the same nodeID simultaneously. The two mutexes are independent objects, so
  "double lock" is real: no shared serialization point between them.

`FileGuard.guards`' gate: `refs == 0 && !s.inProgress.Load()`.
- `inProgress` is NOT independent of the thing `Release` is ordering against itself: it is the
  *exact same flag* whose "cleared" state is a precondition for the eviction gate to pass. Unlike
  btree's `version`, there is no independent piece of state that could be zero/absent while the
  "protected" condition (currently in-progress) is still true.

## Direct experiment (this run)

Wrote a temporary whitebox probe test (not committed; deleted immediately after running) inside
`package split` that manually reproduces the *reversed* order end-to-end:

```go
g.TryAcquire(fileID)                 // winner
s := g.guards[fileID]                // whitebox peek
g.releaseGuard(fileID, s)            // reversed: release BEFORE Store(false)
// assert: entry still present (eviction gate failed, since inProgress still true) -- confirmed
// assert: a subsequent TryAcquire(fileID) during this "stuck" window returns false -- confirmed
s.inProgress.Store(false)            // what the real (correct) Release would have done first
```

Result: `go test -run TestProbeReversedOrderNoDoubleAcquire -race` passed both assertions --
the entry survives (matches verifier's finding: reversed order breaks eviction), AND, critically,
**no double-acquisition occurs**: a `TryAcquire` racing into the reversed-order window still
observes `inProgress == true` (nobody cleared it yet) and its `CompareAndSwap(false, true)`
correctly fails, so it loses as expected.

## Why this refutes the doc comment's claim (not just "didn't reproduce it")

This is not merely "the verifier's specific mutation didn't happen to hit the claimed window" --
it is structurally impossible for the claimed scenario to occur, for any interleaving, because:

1. The eviction gate reads `s.inProgress.Load()` directly. As long as `inProgress == true`, the
   gate cannot pass (`!inProgress` is false), so the entry is never deleted from `g.guards` and no
   fresh replacement entry can be created for that `fileID` regardless of ordering.
2. Eviction only ever becomes possible once `inProgress` has already been observed `false` by
   `releaseGuard`'s own gate check. But `inProgress` only transitions true->false via `Store(false)`
   inside `Release`. So by the time eviction is possible at all, some `Release` call's
   `Store(false)` has already executed for that fileID -- meaning any subsequent `TryAcquire`
   racing against the *same* winner's now-completing `Release` sees `inProgress` genuinely false
   and correctly wins fresh (this is the intended "next split can start" behavior, not a bug).
3. Contrast with btree: there, `version` (the gate's OTHER clause) is entirely decoupled from
   `l.mu`'s locked/unlocked state, so unlocking the mutex does not, by itself, prevent the gate
   from passing on the next line. That decoupling is exactly what FileGuard's design does NOT have
   -- its gate's blocking clause IS the flag being ordered against.

Conclusion: **path (b)** applies. The "load-bearing... prevents double-acquisition" claim in
`Release`'s doc comment is not just underproven, it is actually false as a description of the
current code -- reversing the order breaks eviction *progress* (confirmed by the verifier via
`TestFileGuardRegistryBounded`, and re-confirmed here), but creates no reachable mutual-exclusion
violation. The doc comment must be corrected to state the real constraint, and a permanent,
non-throwaway regression test must capture both halves of this (entry retained under reversed
order; and, separately/explicitly, that no double-acquisition results even in the retained-but-
stale state) so the corrected claim is provable, not merely asserted.

## Secondary finding investigation

`TestFileGuardRegistryRetainsInProgressEntries` relies on `TryAcquire`'s real winning path to
create a `refs>0, inProgress=true` entry, then never releases it. Its retained-entry assertion is
therefore consistent with *either* `refs>0` alone (refs clause) or `!inProgress` alone (inProgress
clause) causing retention -- it cannot distinguish them. Mutation-testing (verifier's finding,
independently reproducible): deleting the `!inProgress` clause from the gate, leaving only
`refs==0`, still passes this test, because `refs` never drops to 0 for `heldFileID` during the
whole test (the winning `TryAcquire`'s own reference is never released).

Fix: add a whitebox test in the same package that constructs the `refs==0, inProgress==true` state
directly (via `acquireGuard`+immediate matching `releaseGuard` to get `refs` back to 0, while
`inProgress` is separately forced true), isolating the `!inProgress` clause from `refs`. This
requires package-internal access, which `guard_test.go` already has (`package split`, not
`package split_test`).
