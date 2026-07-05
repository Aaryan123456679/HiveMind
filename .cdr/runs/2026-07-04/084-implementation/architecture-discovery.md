# Architecture discovery — 2a.2.2

## Index-first
- `.cdr/index/task.jsonl`: task-2a.2.1 verified (EpochManager standalone, deliberately
  unwired). task-2a.1.* all verified (Snapshot/CommitVersion baseline behavior).

## engine/mvcc/gc.go (2a.2.1, current state)
- `EpochManager{mu, current uint64, refcounts map[uint64]int64}`, single global
  (store-wide, not per-fileID) monotonic epoch counter starting at 1.
- `AcquireCurrentEpoch()` bumps refcounts[current]++ and returns current.
- `Release(epoch)` decrements, deletes map entry at 0, errors on double-release.
- `MinReferencedEpoch()` returns smallest epoch with refcount>0, ok=false if none.
- Doc comments already state the intended wiring: NewSnapshot -> AcquireCurrentEpoch,
  a future Snapshot.Close -> Release, CommitVersion -> AdvanceEpoch after each
  successful CAS. This subtask executes exactly that, plus the compactor.

## engine/mvcc/read.go (Snapshot / NewSnapshot — no epoch integration yet)
- `Snapshot{vw, fileID, version}`. `NewSnapshot(cat, vw, fileID)` calls `cat.Get`,
  pins `version = rec.CurrentVersion`. No `Close()` method exists.
- `SnapshotRead` = one-shot `NewSnapshot` + `Read`.
- Call sites needing update for new signature: read_test.go
  (TestSnapshotRead, TestSnapshotReadNoVersionCommitted), mvcc_test.go
  (TestConcurrentReadersWriters).

## engine/mvcc/write.go (CommitVersion / VersionWriter)
- `VersionWriter{dir, states sync.Map, commitLocks sync.Map}`. `WriteVersion` assigns
  strictly increasing, never-reused version numbers per fileID under `states`'
  per-fileID `fileState.mu`.
- `CommitVersion(cat, w, fileID, data)` loops: `cat.Get` -> `WriteVersion` -> `walCAS`
  (WAL-log then CAS under `commitLocks`); on lost race, retries with a FRESH version
  file (old number never reused → orphaned "loser" files are possible, with version
  numbers interleaved with successfully-committed ones).
- Important invariant reused by the compactor: once all in-flight commits for a fileID
  finish, `CurrentVersion` == highest version number on disk (see doc comment); an
  orphaned loser's version number, once it exists, is *never* current and is
  immediately superseded by whatever wins next.
- Call sites needing signature update: write_test.go, read_test.go, mvcc_test.go (all
  call `vw.CommitVersion(cat, w, fileID, data)`).

## engine/catalog/catalog.go
- `Catalog.Get(fileID) (CatalogRecord, error)` — CatalogRecord.CurrentVersion is the
  authoritative "current version" pointer; compactor must never delete this file
  regardless of anything else.

## docs/LLD/mvcc.md — "Garbage collection"
> Old versions are reclaimed by a background compactor once no in-flight reader still
> holds a snapshot referencing them. Uses reference-counted snapshot epochs ... each
> snapshot increments an epoch's refcount on start and decrements on completion; a
> version is eligible for GC once its epoch's refcount reaches zero and it is not the
> current version.

This confirms: (1) refcount-zero-and-not-current is the exact acceptance criteria: (2)
"epoch's refcount" language implies each version must be associated with a specific
epoch (the one it was superseded at), which is exactly the mapping problem plan.md
below works out.
