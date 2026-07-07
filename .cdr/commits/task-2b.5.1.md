# task-2b.5.1 — TestConcurrentAppendSplitRace (issue #14, subtask 1 of 2)

## Summary

First subtask under GitHub issue #14 ("[2b] Auto-split concurrent race-test suite", Epic Phase 2b: Auto-split,
the epic's final issue). Adds `TestConcurrentAppendSplitRace` to `engine/split/split_race_test.go`: many
goroutines append concurrently to the same file under real contention (`-race` exercised), and the test asserts
no data loss, exactly one split fires per threshold crossing, and no dangling graph edges result. The real
split machinery (`Orchestrator.BeginSplit` → `ExecuteSplitAtomic`) is driven for real, not mocked.

While investigating the real integration seam prior to writing the test, the implementer found and fixed a
genuine pre-existing production bug (see Impact) rather than working around it in the test.

## Features

- `TestConcurrentAppendSplitRace` (`engine/split/split_race_test.go`): concurrent-append stress test with a
  precomputed tag-level oracle, checked for exact-one-split-per-crossing and a `redirectCount == splitCount`
  invariant, plus a full graph edge `Source`/`Target` → catalog walk to rule out dangling edges.
- Supporting helpers added in the same file: `driveSplitRound`, `countRedirectRecords`, `collectLeafTags`.

## Impact

- **Real bug found and fixed (not test-only):** `ExecuteSplitAtomic` and `RecoverSplitCommits`
  (`engine/split/execute.go`) never created a `catalog.CatalogRecord` for newly-split-off fileIDs. Split-off
  files were permanently unreadable/unappendable (`cat.Get` returned `ErrNotFound`) even though btree/graph/
  content-store state already referenced them as live. Fixed by adding a `cat.Put(newRec)` loop (status
  `StatusActive`) to both the live commit path and the crash-replay path, and by threading a new `SizeBytes`
  field through the WAL's `SplitCommitEntry` (`engine/wal/record.go`) so crash-replay can reconstruct correct
  catalog records with no other source of truth available at that point. This was found by direct source
  reading during architecture discovery, before the race test was ever run — not a bug the test happened to
  trip over.
- A latent, harmless test-fixture bug (hardcoded `originalFileID` constant in `TestSplitAtomicCommit`'s
  `newDeps` helper, which would have collided with real allocator-issued fileIDs once the above fix landed)
  was also found and fixed in the same pass; confirmed structurally impossible in production, where all
  fileIDs originate from the same `idAlloc` sequence.
- No regressions: full `engine/split/...` and `engine/...` suites (including `-race`) green throughout.

## Verification

- **Verdict**: PASS (as part of the combined issue #14 gate).
- **Run**: `.cdr/runs/2026-07-07/037-verification/verification.json` (`requirements` dimension: pass,
  `bug_fix_correctness`: pass — traced both `ExecuteSplitAtomic` and `RecoverSplitCommits`, confirmed identical
  `SizeBytes`/catalog-record handling, confirmed no production path depends on the record's prior absence).
- 2b.5.1 was never in dispute across the issue's fix cycle; the CHANGES_REQUESTED verdict on issue #14
  (`.cdr/runs/2026-07-07/035-verification/verification.json`) applied solely to 2b.5.2 (see task-2b.5.2.md).

## Release Notes

Adds a genuine concurrent-append/split race test (`-race`-clean) covering no-data-loss, exactly-once-split, and
no-dangling-edges guarantees for the auto-split pipeline, and fixes a real production bug uncovered along the
way: newly-split-off files were unreadable/unappendable because no catalog record was ever created for them on
either the live commit path or crash-recovery replay. No breaking API changes.
