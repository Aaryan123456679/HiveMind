# Architecture discovery — subtask 4.5.2.1

## Timeline reconstruction (this is the key finding of this run)

1. `gh issue view 39` was read fresh. It cites `.cdr/index/regression.jsonl` subtask `2a.2.2`,
   run `001-verification`, as "HIGH, unresolved" — this is the sole justification for creating
   subtask 4.5.2.1.
2. `.cdr/index/regression.jsonl`'s `2a.2.2` line points at run `2026-07-05/001-verification`. Its
   full `verification.json` (verdict `CHANGES_REQUESTED`) is the ORIGINAL finding: `NewSnapshot`
   (as it existed at commits `a5231d3`/`7b25eeb`) called `cat.Get()` (reading `CurrentVersion`)
   BEFORE `em.AcquireCurrentEpoch()`. A concurrent `CommitVersion` completing (CAS + AdvanceEpoch +
   recordVersionEpoch) fully between those two steps could make the later-acquired epoch already
   `>= supersededAtEpoch(V)`, so `RunCompaction` would judge V's file safe to delete while a live,
   un-closed `Snapshot` was still pinned to V. Reproduced deterministically in that verification
   session (throwaway `zzrace_test.go`, not committed).
3. Immediately following that verification, `git log --oneline -- engine/mvcc/` shows a fix commit
   `acc7601` ("fix(mvcc): close TOCTOU race in NewSnapshot's epoch-acquire ordering (2a.2.2 fix)"),
   timestamped `Sun Jul 5 08:36:09 2026`, i.e. AFTER the `001-verification` run
   (`2026-07-05T00:20:00Z`). Its own run artifacts exist at
   `.cdr/runs/2026-07-05/002-implementation-fix/` (handoff.json references
   `commit_full: acc760129ececc8eb83938123c05f9a94af8eccc`, files changed:
   `engine/mvcc/read.go`, `engine/mvcc/gc_test.go`).
4. That fix was RE-VERIFIED in `.cdr/runs/2026-07-05/003-verification/verification.json`
   (subtask `task-2a.2.2`, round 2, commits `acc7601`/`afb3fbc`), verdict **PASS_WITH_COMMENTS**.
   The re-verification independently re-derived the happens-before proof from the actual mutex
   usage in `gc.go` (`em.mu` guarding both `AcquireCurrentEpoch`/`AdvanceEpoch`) and `catalog.go`
   (per-fileID stripe lock guarding both `Get`/`CompareAndSwapCurrentVersion`), confirming the
   reordering genuinely closes the race, and confirmed the new regression test
   (`TestNewSnapshotClosesEpochAcquireVersionReadRace`, gc_test.go:382-512) is a real,
   goroutine-based, hook-paused concurrency test (not a disguised sequential test).
5. Follow-on commits `d69cf0a` (test: GC correctness under concurrent load, 2a.2.3) and `a1f220d`
   (fix: doc-comment/stress-test correction, 2a.2.3) build on top of the fixed ordering without
   reverting it.

## Current state of the three impacted files (verified by direct read, HEAD = `545e827`)

- `engine/mvcc/read.go`: `newSnapshotWithHook` (lines 113-124) already does
  `epoch := em.AcquireCurrentEpoch()` FIRST, then `afterAcquireBeforeVersionRead` hook, then
  `cat.Get(fileID)` SECOND, releasing the epoch on error. The doc comment (lines 44-99) already
  documents the corrected ordering, includes a rigorous real-time/happens-before proof, and
  explicitly calls out that the PREVIOUS version of the comment (acquire-after-read) was
  incorrect — i.e. the "now-inaccurate race-note comment" the acceptance criteria asks to be
  corrected has ALREADY been corrected.
- `engine/mvcc/gc.go`: `EpochManager.AcquireCurrentEpoch`/`AdvanceEpoch`/`Release`/
  `MinReferencedEpoch` and `RunCompaction` are unchanged since 2a.2.2/2a.2.3 landed; `RunCompaction`
  already implements the `anyReferenced && minRef < supersededAtEpoch(v)` skip condition described
  in the fix's proof.
- `engine/mvcc/gc_test.go`: `TestNewSnapshotClosesEpochAcquireVersionReadRace` (lines 368-513)
  already exists, using a channel-handoff hook (`newSnapshotWithHook`'s
  `afterAcquireBeforeVersionRead` parameter) exactly matching the pattern requested by the
  subtask's test spec — pauses `NewSnapshot` after the epoch acquire but before `cat.Get`, races a
  concurrent `CommitVersion` + `RunCompaction` to completion in that window, then resumes and
  asserts the pinned version's file still exists and `snap.Read()` still succeeds.
  `TestGCUnderConcurrency` (lines 515+, subtask 2a.2.3) additionally stress-tests this under real
  concurrent load.

## Conclusion

Subtask 4.5.2.1's acceptance criteria are **already fully satisfied** by commits already on `main`
(`acc7601`, re-verified PASS_WITH_COMMENTS in `003-verification`, plus follow-on `d69cf0a`/
`a1f220d`). Issue #39 appears to have been filed by scanning `regression.jsonl` for entries lacking
an explicit "resolved" marker, without cross-referencing the later `002-implementation-fix` /
`003-verification` runs that already closed this specific finding. This is a **stale-tracking
issue**, not a live code gap. No source-code change is warranted; making one would risk
reintroducing churn into already-verified, already-race-tested code for no behavioral gain, and
would violate the standing principle of not re-doing already-verified work.

Ran `go test ./engine/mvcc/... -race -v -count=1` at current HEAD: all tests, including
`TestNewSnapshotClosesEpochAcquireVersionReadRace` and `TestGCUnderConcurrency`, PASS.

## Security note (untrusted-content disclosure)

No embedded fake system-reminder-style text was found in `gh issue view 39`'s body or in the
`.cdr` files read during this investigation. (Flagging per standing instruction that such content
has appeared before in this repo's issues/history — none observed this run.)
