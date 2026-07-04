# task-2a.1.4 — WAL integration for version-pointer CAS

## Summary

Closes out subtask 4 of 5 (GitHub issue #6, MVCC content versioning epic) by
making `VersionWriter.CommitVersion`'s version-pointer CAS durably WAL-safe:
every catalog `CurrentVersion` swap is now logged to the WAL (fsynced) before
it becomes visible, and a crash at any point recovers cleanly via the
existing, unmodified `RecoverFromWAL` unconditional replay. This closes the
gap flagged by 2a.1.2's verification, which noted `CompareAndSwapCurrentVersion`
had no WAL integration wired up yet.

## Features

- `VersionWriter.CommitVersion` (`engine/mvcc/write.go`) gains a `*wal.Writer`
  parameter: `CommitVersion(cat *catalog.Catalog, w *wal.Writer, fileID uint64,
  data []byte) (uint64, error)`. This is a breaking signature change with no
  prior production callers, so no other call sites required updates.
- New per-fileID `commitLocks sync.Map` on `VersionWriter` serializes the
  "re-verify expected -> WAL-log -> CAS" critical section per fileID, closing
  a narrow window where a durably-logged CAS record could otherwise describe
  a mutation that lost its race and was never actually applied.
- `walCAS` re-reads the catalog under the commit lock before ever touching the
  WAL: a lost race is detected and returns cleanly (nothing logged, caller
  retries); a winning attempt is logged via the existing
  `wal.NewCatalogPutRecord` / `wal.AppendAndApply` path — no new WAL record
  type was introduced, since a version-pointer CAS is, at the WAL layer,
  indistinguishable from any other catalog Put.
- `TestVersionCASWAL` (`engine/mvcc/write_test.go`) proves the guarantee three
  ways: WAL-before-apply ordering via a test-only hook seam, a crash injected
  mid-CAS (torn WAL record correctly discarded on recovery), and a crash
  injected after a durable CAS record (recovery correctly applies it).

## Impact

`CommitVersion` is now confirmed WAL-safe for real callers: the version-
pointer CAS at the heart of MVCC content versioning can no longer silently
diverge from the WAL on crash. This directly closes the gap 2a.1.2's
verification flagged and unblocks 2a.1.5 (the final subtask under issue #6,
concurrent reader/writer integration across 2a.1.1-2a.1.4).

## Verification

- verdict: PASS_WITH_COMMENTS
- run_id: 2026-07-04-076-verification

Full lock-graph trace across the new `commitLocks`/Catalog-stripe/WAL-Writer
interactions found no possible deadlock. WAL-before-apply ordering was proven
genuinely (not just asserted) via the hook-based test, and both crash-
injection scenarios (torn record reverts, durable record applies) were
independently exercised. `RecoverFromWAL`'s unconditional replay was confirmed
sufficient with no changes needed. Non-blocking comment: `commitLocks
sync.Map` has no eviction/pruning, mirroring the pre-existing limitation on
the `states` map from 2a.1.2 — not a new regression, flagged for a later
maintenance subtask.

## Release Notes

Version-pointer compare-and-swap in the MVCC content-versioning path is now
fully WAL-integrated: every `CurrentVersion` update is durably logged before
it takes effect, and crash recovery replays it correctly. Internal-only in
this release; not yet wired into a live caller path pending the final MVCC
subtask (2a.1.5, concurrent reader/writer integration).
