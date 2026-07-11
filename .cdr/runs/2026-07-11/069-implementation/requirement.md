# Requirement — subtask 4.5.1.5 (issue #38)

## Acceptance criteria (from macro)
Add two new small, deterministic, hook-forced-interleaving regression tests
(mirroring `TestOptimisticRead`/`ForcedRetryDeterministic`-style patterns
already in `engine/btree`) that independently pin down:

1. `repairEmptyLeafAtParent`'s borrow/merge-into-left branch no longer
   orphans a node when the left sibling concurrently splits under its own
   latch (the "orphan fix").
2. Leaf/internal split "publish-last" ordering prevents `Tree.Lookup`'s
   optimistic read from observing a not-yet-written new node pointer (the
   "publish-last fix").

Test spec:
- `go test ./engine/btree/... -race -run TestRepairEmptyLeafOrphanRegression`
- `go test ./engine/btree/... -race -run TestSplitPublishLastOrderingRegression`

Impacted modules: `engine/btree/delete.go`, `engine/btree/insert.go`,
`engine/btree/delete_test.go`, `engine/btree/insert_test.go`.

## Pre-implementation question: are these fixes already shipped, or still needed?

Searched `git log` for prior commits. Confirmed both underlying fixes shipped
in commit `b31328f` ("fix(btree): resolve concurrent insert/delete propagate
race on shared parent (2a.4.5 blocker)"):

- Bug 2 (orphan): "repairEmptyLeafAtParent's borrow/merge-from-left branches
  could orphan a concurrently-split-but-not-yet-propagated left-sibling
  successor node by blindly overwriting the left sibling's NextLeaf. Fixed by
  detecting the race (`left.NextLeaf != leafID`) and retrying."
- Bug 3 (publish-last): "Leaf/internal split write order let Tree.Lookup's
  lock-free optimistic reads observe a pointer to a not-yet-written new node
  ... Fixed by writing the new node before the node that publishes a
  reference to it."

Both fixes are present, unchanged, in current `engine/btree/delete.go` and
`engine/btree/insert.go` (verified by reading both functions in full). So
this subtask is primarily test-only regression coverage, per the macro's own
"most likely" hint.

HOWEVER: while constructing the deterministic hook-forced test for the
orphan fix, reading `repairEmptyLeafAtParent`'s exact race-detection branch
line-by-line (not just skimming) revealed a second, previously-undetected,
genuinely reachable bug in the SAME branch: the `left.NextLeaf != leafID`
retry path calls `store.Unlock(leftID)` TWICE and never calls
`store.Unlock(leafID)` at all. This leaks `leafID`'s latch forever (any
future `Lock(leafID)` blocks forever) and double-unlocks `leftID`'s mutex
(a guaranteed panic: `sync: unlock of unlocked mutex`, or `NodeStore.Unlock`'s
own "no outstanding Lock/TryLock" panic if the latch entry was evicted in
between). This has been present, unfixed, since commit `b31328f` -- it was
never caught because no prior test ever exercised this exact retry branch
deterministically (the capstone stress tests exercise it only
probabilistically, if at all, and evidently never happened to hit it in a way
that surfaced the panic).

Per this subtask's own instruction to "verify from source and git history,
don't assume" whether production code changes are needed: this finding
means a real, narrow production fix IS required in `engine/btree/delete.go`,
in addition to (and only discoverable via) the new regression test.
`engine/btree/insert.go`'s publish-last ordering, by contrast, is confirmed
correct as shipped -- only new hooks (test-only injection points, mirroring
`unlockOrderHook`'s established idiom) were added there, no logic changed.
