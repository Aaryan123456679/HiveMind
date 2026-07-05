# Plan — 2a.2.2

## (a) Wiring

1. `Snapshot` (read.go) gains `em *EpochManager` and `epoch uint64` fields.
   `NewSnapshot(cat, vw, em, fileID)`: after `cat.Get` resolves `rec.CurrentVersion`,
   call `epoch := em.AcquireCurrentEpoch()` (in that order — see race analysis below)
   and store it. Add `Snapshot.Close() error` that calls `em.Release(s.epoch)` exactly
   once. `SnapshotRead` becomes a one-shot: `NewSnapshot` -> `defer snap.Close()` ->
   `Read()`.

   Race analysis (cat.Get-then-Acquire, not atomic together): between reading
   `CurrentVersion` and acquiring the epoch, a concurrent commit could advance the
   global epoch. This can only make the acquired epoch *newer* than "the epoch
   in effect when the version was read" (epochs are monotonic, only ever increase), so
   the snapshot always advertises a reference at least as protective as needed, never
   less. Effect: possibly delayed reclamation (over-conservative), never premature
   reclamation (unsafe). Full concurrent stress-testing of this window is 2a.2.3.

2. All existing Snapshot-creating call sites must now `Close()`: an epoch acquired and
   never released leaks forever under this refcounting model, permanently pinning
   `MinReferencedEpoch()` and defeating the compactor. Updated:
   - `read_test.go`: `TestSnapshotRead` (holds `snap` across a `defer snap.Close()`),
     `TestSnapshotReadNoVersionCommitted` (SnapshotRead already self-closes).
   - `mvcc_test.go`: `TestConcurrentReadersWriters`'s reader loop creates a fresh
     Snapshot every iteration — each must `Close()` before looping (via a small
     closure or explicit call after Read/before next iteration), not deferred (that
     would pile up thousands of deferred closes for the loop's lifetime; each
     iteration's Snapshot is only needed for that one Read).

3. `CommitVersion(cat, w, em, fileID, data)` (write.go): after a successful `walCAS`
   (i.e. right before returning `version, nil`), call `epoch := em.AdvanceEpoch()` and
   record `(fileID, version) -> epoch` via a new `VersionWriter.recordVersionEpoch`
   helper backed by a `sync.Map` of fileID -> `*sync.Map` (version -> epoch), mirroring
   the existing `states`/`commitLocks` per-fileID sync.Map shape.

4. Epoch<->version mapping design (documented in gc.go): epoch numbering is GLOBAL
   (shared across all fileIDs), so a single "epoch == version number" identification is
   NOT valid across fileIDs. Instead: for fileID F, `versionEpochs[F][v]` records the
   epoch AdvanceEpoch returned for the commit that made `v` current for F — i.e. "the
   epoch as of which the PREVIOUS version was superseded". The compactor looks up the
   *next* recorded version's epoch (`nextRecordedVersionEpoch(fileID, v)`, smallest
   recorded version number > v) as v's "superseded-at epoch"; this correctly bridges
   gaps left by orphaned "losing" CommitVersion retries, which never get a recorded
   epoch of their own (their number is skipped straight to the next successfully
   committed version).

   Known limitation (documented in code): this map is in-memory only, scoped to one
   VersionWriter's process lifetime; it does not survive a restart. Persisting it is
   out of scope for this subtask (not required by the acceptance criteria / test spec)
   and flagged for a future subtask if crash-safe reclamation across restarts is
   needed.

## (b) Compactor

`RunCompaction(cat, vw, em, fileID) ([]uint64, error)` in gc.go:
1. `rec, _ := cat.Get(fileID)`; `currentVersion := rec.CurrentVersion`.
2. `versions, _ := vw.listVersionFiles(fileID)` (new helper, mirrors the existing
   `scanLatestVersion`/`countVersionFiles` filename-parsing pattern already used
   elsewhere in the package).
3. `minRef, anyReferenced := em.MinReferencedEpoch()`.
4. For each on-disk version `v`:
   - `v == currentVersion`: always skip (never reclaim current, regardless of
     refcount — this is tested explicitly).
   - `v > currentVersion`: skip (in-flight/uncommitted or unresolvable orphan; not
     safe to reason about).
   - `supersededAtEpoch, ok := nextRecordedVersionEpoch(fileID, v)`; `!ok`: skip
     conservatively (unknown epoch history).
   - Delete iff `!anyReferenced || minRef >= supersededAtEpoch`; otherwise skip. (A
     live snapshot's acquired epoch is always >= the epoch at which whatever it
     actually pinned became current, so `minRef >= supersededAtEpoch` means every live
     snapshot acquired its epoch at-or-after `v`'s supersession, and therefore was
     necessarily looking at `v`'s successor or later — never `v` itself.)
   - Delete via `os.Remove`; treat `os.IsNotExist` as a benign no-op (idempotent under
     concurrent/duplicate compaction passes).
5. Return the list of deleted version numbers.

Concurrency-safety reasoning (for 2a.2.3 to build on): RunCompaction never locks
per-fileID; it only ever deletes files that are provably unreachable by any currently
live (or future, since new snapshots only ever acquire >= current epoch) snapshot, and
delete-of-already-deleted is a harmless no-op. It never touches `states`/`commitLocks`,
so it cannot deadlock or contend with concurrent WriteVersion/CommitVersion beyond
filesystem-level rename/remove ordering (which is already atomic per-OS).

## Test plan (gc_test.go, TestCompactor)

Commit v1..v4 for one fileID. Open (don't close) a Snapshot right after v3 commits
(pins version 3, acquires v3's epoch) before committing v4. Run RunCompaction; assert:
- v1, v2 deleted (superseded strictly before the held snapshot's epoch).
- v3 retained (the held snapshot's epoch is still live).
- v4 (current) retained.
- Explicit sub-case: even after closing the held snapshot so refcount hits zero, v4
  (current) is STILL retained on a second RunCompaction call — proving "current is
  never reclaimed" is independent of refcount, not just incidentally true because it
  always happens to have live references in this test.
- After closing the held snapshot, a second RunCompaction call now also reclaims v3.
