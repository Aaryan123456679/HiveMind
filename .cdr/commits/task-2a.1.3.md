# task-2a.1.3 — Snapshot-read path capturing version pointer at request start

## Summary
Closes out subtask 3 of 5 (GitHub issue #6, MVCC content versioning epic) by
implementing the read side of MVCC: a `Snapshot` that pins a `fileID` to the
catalog's current version number at one instant, then serves that exact
version's content to completion regardless of any concurrent writer advancing
the pointer afterward. This is the "Read path" contract from
`docs/LLD/mvcc.md`: a reader must keep seeing its originally-snapshotted
version for the whole read, even while writers commit newer versions
underneath it.

## Features
- `Snapshot` / `NewSnapshot` (`engine/mvcc/read.go`): captures a `fileID`'s
  `CurrentVersion` from the catalog at the moment of the call and pins the
  snapshot to that version number for its lifetime.
- `Snapshot.Read` / `Snapshot.Version`: reads the pinned version's immutable
  content file to completion, with no locking required on the read side.
- `SnapshotRead`: one-shot convenience combining capture-and-read for callers
  that don't need to hold the `Snapshot` value separately.
- Correctness rests on a documented, verified invariant: version files are
  never rewritten once written (2a.1.1) and nothing yet deletes old versions
  (epoch-based GC is a distinct, not-yet-built subtask, 2a.2), so a
  snapshotted version's file is guaranteed to exist unchanged for the entire
  read.
- `TestSnapshotRead` proves this under real interleaving: it pauses a read
  right after the version is pinned but before bytes are read, races a
  concurrent `CommitVersion` to completion in that window, then asserts the
  resumed read still returns the pre-commit content.

## Impact
`engine/mvcc/` now has a working, race-tested snapshot-read path, giving
readers a consistent view of a file's content across the duration of a
request even under concurrent writes. This unblocks the two remaining
subtasks under issue #6: 2a.1.4 (WAL integration for the version-pointer CAS)
and 2a.1.5 (concurrent reader/writer race test).

**Scope note:** as with 2a.1.1/2a.1.2, this snapshot-read path is not yet
wired into any real caller / production request path. The load-bearing
assumption that no codepath deletes or mutates a version file a snapshot
might be reading holds for the current codebase, but is only airtight until
epoch-based garbage collection (2a.2) lands — that subtask must explicitly
account for in-flight snapshots when reclaiming old versions.

## Verification
- verdict: PASS_WITH_COMMENTS
- run_id: 2026-07-04-073-verification

Non-blocking comment from verification: no test currently exercises a
snapshot surviving multiple (2+) subsequent commits during its lifetime,
only one. Recommended as a follow-up strengthening test, not required by the
stated acceptance criteria.

## Release Notes
Added a race-safe snapshot-read path for MVCC content versioning: a reader
now pins the version it sees at the start of its request and keeps reading
that exact version to completion, unaffected by concurrent writers
publishing newer versions. Internal-only in this release: not yet wired
into any live read path pending remaining MVCC subtasks (WAL integration
and end-to-end concurrency testing).
