# task-2a.4.3 — Latch-crabbing delete

## Summary

Third of 5 subtasks under task-2a.4 (B-tree latch-crabbing concurrency, GitHub issue #9). Adds concurrent, hand-over-hand ("latch-crabbing") delete to `engine/btree`, reusing the `TryLock` + full-release + restart-from-root discipline that 2a.4.2's insert established the hard way (across four rounds, including a proven deadlock fix), rather than reinventing a parallel locking scheme for delete.

This subtask took one fix round to close:

1. **Round 1** (`b46d466`) shipped the initial design: `Tree.Delete` built on a deliberately duplicated (not refactored) window-of-2 `crabDeleteOnce` descent, widened to a 3-latch window (parent, empty leaf, chosen sibling) for leaf-level borrow/merge, and an internal-node degenerate-splice/root-collapse cascade structurally identical to insert's `propagate` climbing loop. It also fixed a latent bug in the single-threaded Phase-1 `delete.go` (dropping `NextSibling`/`LowKey` on reconstructed internal nodes) for the new concurrent path. Re-verification (`005-verification`) returned **CHANGES_REQUESTED**: the splice's dangling-`NextSibling` repair only handled the same-grandparent case (`gj > 0`); whenever the spliced ancestor was its grandparent's *first* child (`gj == 0`), the true left neighbor lives under an adjacent grandparent and was deterministically never patched — not a probabilistic race, a guaranteed miss. Since `NextSibling` is load-bearing (dereferenced for move-right routing by `crabDeleteOnce`, `crabInsertOnce`, and `findParent` alike) and node IDs are never freed/reused, this was a standing, permanent corruption risk, not a transient window.
2. **Fix round 1** (`f949d54`) rewrote `findLeftNeighborAtSameLevel` to walk up the tree one level at a time via `findParent` until it finds an ancestor that is *not* its own parent's first child, then descend back down taking the last child exactly as many levels as it walked up — correctly locating the true left neighbor whether it's a same-grandparent sibling or lives under an entirely different subtree, generalizing beyond the single-level `gj == 0` case. It also fixed the regression test, which had called the single-threaded free-function `Delete()` and therefore never actually exercised the concurrent `spliceOutDegenerateAncestor` path it was meant to test; it now goes through `Tree.Delete` via `NewTree`.

Final re-verification (`007-verification`) confirmed the fix by independent hand-trace of the walk-up/descend-down index arithmetic for `gj > 0`, single-level `gj == 0`, and (via a temporary, non-committed adversarial test) double-level-nested `gj == 0`, all correct; lock discipline sound (no self-deadlock, one fresh latch per hop); the corrected regression test now genuinely exercises the concurrent path and is non-tautological; full `engine/btree` and whole-module test suites clean under `-race`.

## Features

- `Tree.Delete`: concurrent, hand-over-hand latch-crabbing delete, reusing insert's `TryLock` + full-release + restart-from-root discipline (`errRestartFromRoot`/`crabRetryBackoff`/`crabRetryHook`) and `findParent` unmodified.
- 3-latch window leaf-level borrow/merge repair (parent, empty leaf, chosen sibling) — delete's extra complexity versus insert's 2-latch split, since merging touches two sibling leaves plus their shared parent; every latch beyond the first two acquired via `TryLock` only, preserving deadlock-freedom by the same construction as insert.
- Internal-node degenerate-splice/root-collapse cascade, structurally identical to `propagate`'s one-latch-at-a-time climbing loop.
- `findLeftNeighborAtSameLevel`: generalized walk-up/descend-down algorithm that correctly locates a spliced ancestor's true left neighbor across arbitrary levels of first-child nesting (`gj == 0` at one or more levels), not just the same-grandparent case.
- Fixes a latent Phase-1 (single-threaded) `delete.go` bug — dropped `NextSibling`/`LowKey` on reconstructed internal nodes — in the new concurrent path, since concurrent test scenarios readily produce multi-node internal levels where the bug would corrupt the sibling chain.
- Regression test corrected to exercise the actual concurrent path (`Tree.Delete` via `NewTree`) rather than the single-threaded free function, with a non-tautological independent re-derivation of the level-2 `NextSibling` chain.

## Impact

- `engine/btree` gains multi-writer concurrent delete via latch-crabbing, closing the last correctness gap (dangling `NextSibling` on ancestor splice) that blocked this path.
- Establishes the generalized `findLeftNeighborAtSameLevel`-style walk-up/descend-down pattern as the correct way to locate a same-level neighbor across arbitrary first-child nesting; any future code touching sibling-chain repair after a splice/merge should reuse this pattern rather than a same-grandparent-only shortcut.
- `task-2a.4` (parent) remains `planned` — subtasks 2a.4.4 and 2a.4.5 are still pending. 2a.4.5 (mixed workload) exercises insert/delete concurrently and directly depends on this delete path's `NextSibling` correctness.
- One non-blocking, tracked follow-up: the multi-level-nested `gj == 0` case was verified only via a temporary, non-committed test during this verification pass. A committed regression test for that case (e.g. parameterizing the existing test over nesting depth, or adding the temp test as a starting point) is recommended so a future refactor of `findLeftNeighborAtSameLevel`'s walk-up loop can't silently regress the multi-level case while the single-level shipped test still passes. See `.cdr/memory/pending.md`.

## Verification

- **Verdict**: PASS
- **Run ID**: `007-verification`
- All dimensions PASS except `test_coverage`, which is PASS_WITH_COMMENTS for the reason above (multi-level-nested `gj == 0` coverage currently exists only as a deleted temporary test, not a committed one).
- Fix independently hand-traced correct for `gj > 0`, single-level `gj == 0`, and double-level-nested `gj == 0` (via temporary adversarial test, 10x `-race` repeat, clean).
- Lock discipline confirmed sound: first iteration reuses the caller's already-locked grandparent by value (no self-deadlock), every subsequent hop locks exactly one fresh node at a time.
- Corrected regression test confirmed to genuinely exercise the concurrent `Tree.Delete` path and to be non-tautological.
- Full `engine/btree` suite green under `-race` (including 15x/20x repeat runs of the targeted delete tests), plus a clean whole-module `go test ./... -race` run.
- Confidence: HIGH.

## Release Notes

`engine/btree` now supports concurrent, multi-writer delete via latch-crabbing, reusing the deadlock-free `TryLock`/restart-from-root discipline established for insert. This closes a fix-cycle finding from the prior round: a dangling-`NextSibling` bug where an ancestor spliced out during delete, if it happened to be its grandparent's first child, could leave a stale sibling pointer that live concurrent operations would follow into an abandoned, unreachable node. No public API change; this is an internal concurrency-correctness fix to the B-tree delete path. One non-blocking follow-up is tracked: add a committed test for the multi-level-nested first-child case, currently verified only via a temporary test during this verification.
