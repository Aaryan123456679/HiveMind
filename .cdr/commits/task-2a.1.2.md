# task-2a.1.2 — Atomic CAS swap of catalog's current-version pointer post-durable-write

## Summary
Closes out subtask 2 of 5 (GitHub issue #6, MVCC content versioning epic) by
implementing the catalog's compare-and-swap of the current-version pointer that
runs after a new content version has been durably written to disk. Given a
`fileID`, an expected current version, and a new version number, the catalog
atomically advances its "current version" pointer for that `fileID` only if the
pointer still matches the expected value at the moment of the swap — guaranteeing
that concurrent writers racing to publish successive versions of the same file
never silently clobber one another's updates.

## Features
- Atomic per-fileID CAS of the catalog's current-version pointer
  (`CompareAndSwapCurrentVersion`), guarded by locking that eliminates the
  check-then-set race window between reading the current pointer and updating it.
- Retry-loop-friendly contract: a failed CAS returns enough information for a
  caller to re-read the current version and retry, and the retry loop is provably
  terminating — the writer that ultimately produces the globally highest version
  number for a `fileID` cannot lose its own CAS attempt.
- `CommitVersion` orchestration in the MVCC layer that composes "write the new
  version file" with "CAS-publish it as current," so a durable write is only ever
  exposed as the current version once the pointer swap has actually succeeded.
- Verified under true concurrency: `TestCurrentVersionCAS` (raced, count=10, zero
  flakes) drives N concurrent writers at the same `fileID` and proves both that no
  writer's on-disk data is lost and that the pointer converges correctly.

## Impact
The catalog now has a race-free way to publish a new content version as
"current" once it has been durably written, which is the second of five
foundational pieces for MVCC content versioning (issue #6). This unblocks the
remaining read-path, CAS-retry-loop, and GC subtasks (2a.1.3-2a.1.5) that depend
on a trustworthy current-version pointer.

**Important safety note — not yet wired to real callers:** `CommitVersion` has no
Write-Ahead-Log (WAL) involvement in this subtask. WAL-before-CAS durability
sequencing is deliberately deferred to subtask 2a.1.4. Until 2a.1.4 lands,
`CommitVersion` must not be wired into any real caller / production code path —
doing so today would let a version be marked "current" without a WAL record ever
existing for it, defeating crash-recovery guarantees. This is a pre-acknowledged,
non-blocking gap called out explicitly in verification (architecture_conformance:
pass_with_comment), not an oversight.

## Verification
- verdict: PASS_WITH_COMMENTS
- run_id: 2026-07-04-070-verification

## Release Notes
Added an atomic compare-and-swap for the catalog's current-content-version
pointer, letting concurrent writers safely publish successive versions of the
same file without losing updates. Internal-only in this release: not yet wired
into any live write path pending WAL-before-CAS sequencing (upcoming subtask).
