# Architecture discovery — 2a.2.3

Index-first: `.cdr/index/task.jsonl` shows task-2a.2.1 and 2a.2.2 verified
(PASS_WITH_COMMENTS), commits 3579336 / acc7601. No existing task-2a.2.3 entry.

## Read source (per protocol, after indexes/handoffs exhausted)

- `engine/mvcc/gc.go`:
  - `EpochManager`: mutex-guarded `current` epoch counter + per-epoch refcount map.
    `AcquireCurrentEpoch()` bumps refcount for the epoch handed out;
    `Release(epoch)` decrements/deletes; `MinReferencedEpoch()` returns smallest
    epoch with a live ref.
  - `RunCompaction(cat, vw, em, fileID)`: lists all version files on disk for
    fileID, skips the current version and anything > current, looks up the epoch
    each version was superseded at (`vw.nextRecordedVersionEpoch`), and deletes a
    version iff `!anyReferenced || minRef >= supersededAtEpoch`. Safe to call
    concurrently with writers/other compaction passes (os.Remove not-exist is a
    benign no-op; idempotent).

- `engine/mvcc/read.go`:
  - `Snapshot` pins a fileID to a version captured at `NewSnapshot` time.
    `NewSnapshot` acquires the epoch **before** reading `CurrentVersion` (2a.2.2's
    TOCTOU fix) — proven race-free in the doc comment: any commit that supersedes
    the pinned version must AdvanceEpoch strictly after the epoch this Snapshot
    acquired, so `RunCompaction`'s skip condition always holds while the Snapshot
    is open.
  - `Snapshot.Close()` releases the acquired epoch (must be called exactly once).
  - `Snapshot.Read()` reads the pinned version's file directly from disk — no
    locking needed since version files are immutable once written and (per
    2a.2.1/2a.2.2) never deleted while any snapshot's epoch still protects them.
  - `SnapshotRead` = NewSnapshot + Read + deferred Close, one-shot convenience.

- `engine/mvcc/write.go`:
  - `CommitVersion(cat, w, em, fileID, data)`: WAL-before-apply CAS loop; on
    success calls `em.AdvanceEpoch()` once and records
    `recordVersionEpoch(fileID, newVersion, epoch)`. Retries with a fresh
    `WriteVersion` on lost CAS race (never reuses a version number). Safe for
    concurrent callers on the same fileID (serialized internally via
    `commitLocks` per fileID).

## Existing tests to reuse rather than duplicate

- `write_test.go`: `newTestCatalog(t) *catalog.Catalog`, `newTestWAL(t, dir) (w
  *wal.Writer, walDir string)` — standard per-test catalog/WAL setup helpers.
- `gc_test.go`: `versionExists(t, vw, fileID, v) bool`, `containsVersion(deleted,
  v) bool` — reusable assertion helpers for on-disk version file existence and
  compaction-return-value membership.
- `gc_test.go`'s `TestCompactor` and `TestNewSnapshotClosesEpochAcquireVersionReadRace`
  establish the single-fileID setup pattern (`cat.Put` seeding a `CatalogRecord`
  with `CurrentVersion: 0`, then looping `CommitVersion`).
- `mvcc_test.go`'s `TestConcurrentReadersWriters` is the closest existing
  precedent for a goroutine-based stress test structure (writers + readers
  goroutines, `sync.WaitGroup`, shared error collection) — reuse its general
  shape (goroutine counts, `t.Error`/shared error slice under mutex, `-race`)
  rather than inventing a new stress-test idiom.

## Design for TestGCUnderConcurrency

Single shared fileID. Three goroutine groups running concurrently for a fixed
duration/iteration budget:
1. Writers: loop `CommitVersion` with small unique payloads.
2. Long-running readers: `NewSnapshot`, record its pinned version + expected
   content (looked up via `vw.VersionPath` read immediately after acquiring,
   which is safe/race-free precisely because of the acquire-before-read
   epoch ordering proven in read.go), sleep/yield briefly while writers and
   compactor continue running, then `Read()` and assert exact byte-for-byte
   match + no error, then `Close()`.
3. Compactor: loop `RunCompaction` throughout.

Correctness invariant under test: a reader's `Read()` after the "hold open"
window must never error (version file deleted out from under it) and must
never return content other than what was true at acquisition. Failures
recorded via a mutex-guarded slice / `t.Errorf` from goroutines (safe with
`testing.T` since Go 1.21+ allows concurrent `t.Error` calls) checked/reported
at the end.
