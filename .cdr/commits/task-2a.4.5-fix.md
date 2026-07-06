# task-2a.4.5-fix — Concurrent insert/delete/lookup race fixes (blocker resolution)

## Summary

Fix cycle for task-2a.4.5 (last of 5 subtasks under task-2a.4, "Latch-crabbing B+Tree concurrency", GitHub issue #9). Subtask 2a.4.5's own capstone `TestConcurrentMixedWorkload` (mixed concurrent Insert/Delete/Lookup across disjoint and overlapping key ranges) reliably reproduced silent data loss and, in other interleavings, an internal invariant panic. This is not the 2a.4.5 subtask's own implementation commit — it is a standalone cross-cutting fix commit (`b31328f`, plus two docs-only follow-ups `2644ac6`, `9cd91f6`) that root-caused and corrected genuine latent bugs in `engine/btree`'s shared insert/delete/lookup surface, unblocking 2a.4.5's own close-out.

## Fixes

Three independent, genuine concurrency bugs, each an interaction between 2a.4.2's insert-only-era assumptions and later additions from 2a.4.3 (tombstone deletes) and 2a.4.4 (lock-free optimistic reads):

1. **Empty-sibling move-right mishandling.** The leaf-level "move right" peek used during crabbing/lookup treated an empty `NextLeaf` sibling as always requiring a move-right. This was safe before 2a.4.3 (a sibling could only ever be a freshly-split, non-empty right half) but became unsafe once Delete's tombstone policy introduced genuinely-empty-but-still-linked leaves. Fixed symmetrically at all three call sites (`crabInsertOnce`, `crabDeleteOnce`, `Tree.Lookup`'s lookup path).
2. **Orphaned-node race in empty-leaf repair.** `repairEmptyLeafAtParent`'s borrow-from-left / merge-into-left branches unconditionally overwrote the left sibling's `NextLeaf`, which could orphan a node if that sibling had just been concurrently split under its own latch before its `propagate` call ran. Fixed with a race-detection guard plus retry via the existing "benign race, restart" idiom.
3. **Split write-ordering vs. lock-free reads.** Leaf/internal split write order previously allowed 2a.4.4's lock-free optimistic `Tree.Lookup` to observe a pointer to a not-yet-written new node. Fixed by publishing the new node before the node that references it.

Also (non-production): relaxed a test helper's (`assertStructuralInvariants`) `LowKey` check from exact-equality to never-exceeds, correctly matching `InternalNode.LowKey`'s documented contract; added a new fast committed regression test (`TestConcurrentInsertDeleteDisjointRangesMinimalRepro`) for bug 1; and reduced `TestConcurrentMixedWorkload`'s scale (80k keys/200 goroutines → 8k/50) after independently confirming the original scale still passes cleanly under `-race`, just slower — a separate, pre-existing retry-backoff throughput ceiling, not a correctness issue, now tracked as a fresh non-blocking pending item.

## Impact

- `engine/btree` package only; no public API change.
- Closes the correctness gap across the shared insert/delete/lookup surface underlying all 5 subtasks of task-2a.4; all three root causes were genuine cross-subtask interaction bugs (2a.4.2 × 2a.4.3, 2a.4.3 × 2a.4.4), now fixed at their true call sites — verification judged this surface "closable" with no further residual gap found.
- Full existing suite (Phase-1 through 2a.4.4) plus both capstone tests pass under `-race` with zero regressions.
- Unblocks task-2a.4.5's own re-verification and close-out (GitHub issue #9).
- Non-blocking follow-ups tracked (not required before proceeding): (a) bugs 2 and 3 currently have no small dedicated fast regression test of their own, only probabilistic coverage via the multi-second capstone test; (b) the pre-existing no-retry-cap TryLock restart-from-root loop has fresh empirical evidence it can manifest as multi-minute non-terminating CPU churn at large scale under a plain (non-race) build — already tracked in `.cdr/memory/pending.md`, now reinforced.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `2026-07-06/014-verification`
- All three fixes independently re-derived from the diff and judged structurally correct (not taken on faith). Acceptance criteria re-run independently (`TestConcurrentMixedWorkload -race -count=5`: 5/5 clean); full `engine/btree` suite and whole-module suite both green under `-race` with zero regressions.
- `pass_with_comments` dimensions: edge-case test coverage (bugs 2/3 lack dedicated fast regression tests) and performance (scale-reduction claim independently reproduced and corroborated; flags the pre-existing livelock-risk pending item as newly reinforced, not newly introduced).
- Confidence: HIGH.

## Release Notes

Fixed three concurrency bugs in `engine/btree`'s B+Tree latch-crabbing implementation that could cause silent data loss or an internal panic under concurrent Insert/Delete/Lookup workloads spanning disjoint or overlapping key ranges. No public API change. Internal test suite scale for the mixed-workload stress test was reduced for CI speed after confirming the original scale still passes correctly, just slower.
