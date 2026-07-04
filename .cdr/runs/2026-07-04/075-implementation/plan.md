# Plan — 2a.1.4: WAL integration for version-pointer CAS

## Design decision: reuse RecordCatalogPut (no new WAL record type)

RecoverFromWAL (engine/catalog/recovery.go) replays RecordCatalogPut records by
decoding the full CatalogRecord blob and calling `cat.Put` unconditionally, in
on-disk order — "last Put wins". A version-pointer CAS, once durably logged,
is exactly "this fileID's CatalogRecord is now X" (X being the record with
CurrentVersion swapped to the new value) — indistinguishable at the WAL layer
from any other catalog Put, and docs/LLD/mvcc.md itself frames it as "a
catalog mutation" that "goes through the WAL first". No new semantics are
needed on replay: reusing `wal.NewCatalogPutRecord(fileID, encoded)` with the
already-updated CatalogRecord is correct and sufficient.

## Design decision: per-fileID "commit lock" inside engine/mvcc, not a
## Catalog.go signature change

The one wrinkle relative to Create/Append's existing WAL-then-apply pattern:
`Catalog.CompareAndSwapCurrentVersion`'s apply step is NOT unconditional — it
can lose a race. If we logged the WAL record and only THEN discovered (inside
`apply`) that the CAS lost, we'd have a durable WAL record describing a
mutation that was never actually applied live. Because replay is
unconditional last-write-wins, a crash landing between that "doomed" record's
durable append and the live retry loop's next (winning) attempt could cause
recovery to reconstruct the losing/stale value instead of the intended final
one.

Two ways to close this gap were considered:
1. Add a `w *wal.Writer` parameter to `Catalog.CompareAndSwapCurrentVersion`
   itself, moving the WAL-log inside its existing stripe-lock critical
   section (guaranteeing the log only happens for an outcome already known to
   succeed, atomically). Rejected for this subtask: it touches
   engine/catalog/catalog.go, outside the subtask's stated impacted modules,
   and would require re-verifying 2a.1.2's already-verified catalog.go
   surface.
2. **Chosen**: introduce a per-fileID commit lock inside `VersionWriter`
   (engine/mvcc/write.go only) that serializes the "re-verify expected still
   holds -> WAL-log -> call CompareAndSwapCurrentVersion" critical section.
   Since `CompareAndSwapCurrentVersion` has no other caller in this codebase
   than this now-serialized path, holding the lock across the WAL append and
   the eventual CAS call guarantees no other goroutine can have raced us
   in between: the CAS inside `apply` is therefore guaranteed to succeed. A
   losing attempt is detected by re-reading `cat.Get` under the commit lock
   BEFORE ever touching the WAL — nothing is logged for it, and the outer
   `CommitVersion` retry loop (unchanged in spirit) tries again with a fresh
   `WriteVersion` call, exactly as 2a.1.2 already documents/tests
   (`TestCurrentVersionCAS` only asserts `fileCount >= numGoroutines`, so
   full serialization of just this narrow step, with `WriteVersion`'s
   expensive I/O still happening outside the lock, does not break it).

## Implementation

- `VersionWriter` gains a `commitLocks sync.Map` field (fileID -> *sync.Mutex),
  mirroring the existing `states sync.Map` pattern used for per-fileID
  version-numbering.
- `CommitVersion(cat *catalog.Catalog, w *wal.Writer, fileID uint64, data
  []byte) (uint64, error)` — new `w` parameter. Delegates to
  `commitVersionWithHook` with a nil test hook.
- `commitVersionWithHook(..., afterWALBeforeApply func())` — same retry loop
  as before (`cat.Get` -> `expected` -> `WriteVersion`), but the CAS step is
  now `vw.walCAS(cat, w, fileID, expected, version, afterWALBeforeApply)`.
- `walCAS`: acquires the fileID's commit lock; re-reads `cat.Get`; if
  `CurrentVersion != expected`, returns `(false, nil)` (lost race, nothing
  logged — caller retries). Otherwise builds the updated `CatalogRecord`
  (CurrentVersion = newVersion), encodes it, and calls
  `wal.AppendAndApply(w, wal.NewCatalogPutRecord(fileID, encoded), apply)`
  where `apply` (a) runs the test-only `afterWALBeforeApply` hook if set, (b)
  calls `cat.CompareAndSwapCurrentVersion(fileID, expected, newVersion)`,
  asserting `ok == true` (hard error, not silently retried, if ever false —
  this would indicate a real synchronization bug, since our own commit lock
  should make this unreachable).
- Doc comments updated to describe the WAL-before-apply guarantee and cross-
  reference docs/LLD/wal.md / docs/LLD/mvcc.md, matching the existing style
  in engine/catalog/content.go.

## Tests (TestVersionCASWAL, engine/mvcc/write_test.go)

1. **WAL-before-apply ordering**: reuse the `afterWALBeforeApply` hook
   technique (mirrors `TestContentCreate` / `TestSnapshotRead`): seed a
   catalog record (CurrentVersion=0), call `commitVersionWithHook` via an
   exported-for-test seam, and inside the hook independently re-read the WAL
   segment from disk (`wal.ReadSegment`) to assert the CatalogPut record for
   this fileID (decoding to confirm `CurrentVersion == newVersion`) is already
   durable, while `cat.Get(fileID).CurrentVersion` is still the OLD value
   (0) — i.e. WAL durable and catalog pointer un-swapped simultaneously.
2. **Crash-inject mid-CAS / recovery**: commit v1 successfully (full
   WAL+apply). Close the wal.Writer. Manually append a torn WAL record
   (crash-injection recipe from `engine/wal/recovery_test.go`'s
   `TestCrashInjectionRecovery`: write a header claiming a large payload
   with only a few payload bytes actually on disk) simulating a crash mid
   v2's CAS (WAL durably logged v2's intended record only partially — torn
   tail). Reopen a fresh Catalog + call `catalog.RecoverFromWAL`, and assert
   the recovered `CurrentVersion == 1` (the torn record is discarded, exactly
   as `RecoverFromWAL`'s underlying `wal.Replay` already guarantees for any
   torn tail) — not corrupted, not partially-applied.
   A second sub-case additionally proves the "if it WAS durably written,
   recovery must apply it" half: commit v1 then v2 (both full, valid,
   non-torn WAL records), reopen fresh Catalog + RecoverFromWAL, assert
   `CurrentVersion == 2`.

## Self-consistency plan

- `go build ./...`, `go vet ./...`, `gofmt -l` clean from `engine/`.
- `go test ./engine/mvcc/... -race -v -count=1` green.
- `go test ./engine/mvcc/... -race -run TestVersionCASWAL -count=5` for
  flakiness.
- `go test ./engine/catalog/... ./engine/wal/... -race -count=1` for
  no-regression (recovery.go/AppendAndApply untouched, but confirm).

## Handoff note

`CommitVersion` becomes WAL-safe as of this subtask: every version-pointer CAS
is now logged to the WAL, fsynced, and durable BEFORE the in-memory/on-disk
catalog pointer swap becomes visible, closing the gap flagged by 2a.1.2's
verification. It is now safe for a future caller-wiring subtask to invoke
`CommitVersion` from real (non-test) code paths, PROVIDED the caller passes
the correct long-lived `*wal.Writer` for the same catalog/WAL directory pair
(no new precondition beyond what `ContentStore` already requires of its own
callers).
