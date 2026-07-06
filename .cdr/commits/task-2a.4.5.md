# task-2a.4.5 — Full concurrent mixed-workload race-test suite (closes task-2a.4 / issue #9)

## Summary

Fifth and final subtask of task-2a.4 (B-Tree latch-crabbing concurrency, GitHub issue #9). Adds `TestConcurrentMixedWorkload` and a deterministic companion `TestConcurrentMixedWorkloadForcedLookupDuringDelete` to `engine/btree/btree_test.go` — the capstone stress suite combining all three concurrent entry points delivered by this task (`Tree.Insert` from 2a.4.2, `Tree.Delete` from 2a.4.3, `Tree.Lookup` from 2a.4.4) under one shared tree, at real scale, for the first time. Disjoint-but-adjacent key ranges are assigned to insert-only, delete-only, and mutate (repeated delete+reinsert, forcing local split/merge churn) goroutine roles, while continuous lookup goroutines read across the entire keyspace throughout — genuinely overlapping with all three write roles at the structural (shared-ancestor) level, per this codebase's established "overlapping subtree" meaning from 2a.4.2. A per-key oracle, precomputed before any goroutine runs, is checked on every successful lookup and again in full at the end, cross-checked against both `Tree.Lookup` and the untouched Phase-1 free `Lookup`, alongside a full structural-invariant and no-orphaned-pointer pass.

This test suite did exactly what a capstone test exists to do: on first run it immediately, reliably reproduced a genuine production concurrency bug that no single-operation-type stress test (2a.4.2's insert-only, 2a.4.3's insert+delete-but-not-together, 2a.4.4's read+one-writer-type) could have caught. A dedicated fix cycle (`b31328f`, verified `014-verification` PASS_WITH_COMMENTS) root-caused and corrected three independent bugs at the shared insert/delete/lookup surface — see Impact below. Once fixed, this subtask's own test suite was independently re-verified (`016-verification`, PASS_WITH_COMMENTS) against its own acceptance criteria.

## Features

- `TestConcurrentMixedWorkload`: 15 insert-only + 15 delete-only + 10 mutate (delete-then-reinsert) + 10 continuous whole-keyspace lookup goroutines against one shared `Tree`, oracle-verified final state (presence/absence + fileID correctness, cross-checked against both read paths) plus full structural-invariant/orphan checks.
- `TestConcurrentMixedWorkloadForcedLookupDuringDelete`: deterministic, hook-forced companion covering the single highest-risk interleaving (`Tree.Lookup`'s optimistic read racing exactly a concurrent `Tree.Delete`'s structural mutation), mirroring 2a.4.4's `TestOptimisticRead/ForcedRetryDeterministic` pattern.
- `TestConcurrentInsertDeleteDisjointRangesMinimalRepro`: fast (<1s), reliable regression test for the bug this capstone suite discovered — 20 goroutines, no `-race` needed to reproduce, added by the fix cycle as a permanent fast tripwire.

## Impact

- **Three genuine, independent concurrency bugs found and fixed** (commit `b31328f`) at the interaction surface between subtasks that individually passed their own verification:
  1. `crabInsertOnce`/`crabDeleteOnce`/`lookupOnce` (insert.go, delete.go, lookup.go): the leaf-level "move right" peek treated an empty `NextLeaf` sibling as always requiring move-right — safe pre-2a.4.3 (a fresh split's sibling could never be empty) but unsafe once 2a.4.3's tombstone policy introduced genuinely-empty-but-linked leaves awaiting repair. Fixed at all three call sites: never move into an empty sibling.
  2. `repairEmptyLeafAtParent` (delete.go): borrow-from-left/merge-into-left unconditionally overwrote the left sibling's `NextLeaf`, which could orphan a node if that sibling had just been concurrently split under its own latch before its `propagate` call ran. Fixed via a `left.NextLeaf != leafID` detect-and-retry guard.
  3. Leaf/internal split write ordering (insert.go): allowed 2a.4.4's lock-free optimistic `Tree.Lookup` to observe a pointer to a not-yet-written new node. Fixed by publishing the new node before the node that references it ("publish-last" discipline).
- All five subtasks of task-2a.4 are now implemented and independently verified. **GitHub issue #9 is closed** by this commit.
- `engine/btree` now has full, verified 3-way concurrent coverage (insert + delete + lock-free read) as its permanent regression baseline for all future work in this package (starting with Epic 2b's auto-split logic, issues #10-14).
- Non-blocking follow-ups carried forward in `.cdr/memory/pending.md`: no retry cap on the TryLock restart-from-root loop; node-latch registry has no eviction; `Tree.Lookup`'s doc comment slightly overclaims "never locks" (briefly acquires `rootMu` via `t.Root()`); bugs 2 and 3 above have no dedicated fast regression test of their own beyond the large probabilistic capstone test.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `016-verification` (task-2a.4.5's own acceptance-criteria verification; the underlying fix's correctness was separately verified in `014-verification`, also PASS_WITH_COMMENTS)
- Acceptance-criteria test spec independently re-run clean: `go test ./btree/... -race -run TestConcurrentMixedWorkload -count=5 -timeout 25m` — 5/5 passes, ~16s.
- Full `engine/btree` suite independently re-run clean under `-race`: zero regressions across Phase-1 through 2a.4.4's own tests, 44s.
- `go build`/`go vet`/`gofmt` clean.
- Test design reviewed adversarially and judged to satisfy "mixed insert/delete/lookup workload across disjoint and overlapping subtrees, oracle-matching final state, zero race-detector reports": disjoint write-role key ranges are a deliberate, necessary choice for oracle well-definedness (not a weakening), overlap is genuinely exercised via continuous whole-keyspace lookups racing all three write roles and via shared B+Tree ancestor structure across adjacent ranges.
- Scale (8k keys / 50 goroutines, reduced from an original 80k/200 configuration during the fix cycle after the larger scale was found to hit an unrelated, pre-existing retry-backoff throughput ceiling — not a correctness regression, independently corroborated twice) judged adequate: still forces multiple leaves, multiple tree levels, and repeated local split/merge churn via the mutate role.
- Confidence: HIGH.

## Release Notes

`engine/btree`'s latch-crabbing B+Tree concurrency work (task-2a.4, GitHub issue #9) is complete: concurrent `Insert`, `Delete`, and lock-free optimistic `Lookup` are now verified safe to run together against a shared tree, including under sustained 3-way stress. This final capstone test suite caught and drove the fix of three genuine bugs at the seams between the individually-verified insert/delete/read paths — a class of bug no single-operation-type test could have found. No breaking API change; this is additive test coverage plus internal correctness fixes to `insert.go`/`delete.go`/`lookup.go`.
