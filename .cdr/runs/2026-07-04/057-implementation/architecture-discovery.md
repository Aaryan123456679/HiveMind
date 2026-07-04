# Architecture discovery — 1.4.4

## Index-first
- `.cdr/index/task.jsonl`: 1.4.1/1.4.2/1.4.3 implemented+verified in `engine/catalog/content.go`.
- `.cdr/index/file.jsonl`, `feature.jsonl`: catalog + WAL modules already indexed (not re-dumped
  here; consulted for file locations only).

## Read: engine/catalog/content.go
- `ContentStore{dir, cat *Catalog, w *wal.Writer, splitThresholdBytes, stripes [256]sync.Mutex}`.
- `OpenContentStore(root, cat, w)` requires an already-open `*Catalog` and `*wal.Writer`; does
  not own their lifecycle (never closes them). This is the seam a "restart" test must drive:
  close the real FileManager/Writer, reopen fresh ones against the same on-disk root, and
  re-wire a new `ContentStore`.
- `Create`/`Append` both use `wal.AppendAndApply(cs.w, walRec, applyFn)` where `walRec =
  wal.NewCatalogPutRecord(rec.FileID, encoded)` — WAL-before-apply already durable per
  fileID's full encoded `CatalogRecord`, one CatalogPut WAL record per Create/Append call.
- `writeContentFile` writes content bytes via temp-file+rename (already durable/atomic).

## Read: engine/catalog/catalog.go
- **Known gap, explicitly documented** (Catalog doc comment, lines ~94-113): `NewCatalog` does
  NOT scan `catalog.dat` on load to rebuild `index map[uint64]location` from what's already on
  disk. A fresh `Catalog` has an EMPTY in-memory index; only records `Put` during the *current*
  process's lifetime are reachable via `Get`/`Delete`. Underlying page bytes ARE durably
  persisted, but the fileID->location index is process-lifetime-scoped only. This doc comment
  explicitly defers "rebuilding the index durably across restarts" to "whichever later subtask
  introduces ... a persisted directory/index page in catalog.dat itself (plausibly alongside
  wal/'s recovery story)" — i.e. exactly this subtask/1.4.4's WAL-replay mechanism.
- Therefore: reopening a `Catalog` against the same `catalog.dat` file alone reconstructs
  nothing. The only path to reconstructing `index` after a "restart" is replaying the WAL and
  re-`Put`ting every surviving `CatalogRecord`.
- No `Recover`/`LoadFromWAL`-type function exists anywhere in `engine/catalog/` (confirmed via
  grep for `Recover|LoadFromWAL|Rebuild`) or `engine/wal/`.

## Read: engine/wal/recovery.go
- `wal.Replay(dir string, apply func(TypedRecord) error) error`: the package's recovery
  entrypoint. Replays every record from the last checkpoint (or from segment 0 offset 0 if no
  checkpoint) in on-disk order, exactly once, validating record type, invoking `apply` per
  record. Already verified (issue #3); no changes needed here — confirmed by re-reading, no gap
  found for this subtask's purposes.
- `wal.TypedRecord.AsCatalogPut() (CatalogPutPayload, error)` and `.AsCatalogDelete()
  (CatalogDeletePayload, error)` (record.go) decode a replayed record's payload back into
  `{FileID uint64, Record []byte}` (Put) or `{FileID uint64}` (Delete). `CatalogRecord.Decode`
  (already exported, used internally by `Catalog.Get`) turns `Record []byte` into a
  `CatalogRecord` ready for `cat.Put`.

## docs/LLD/wal.md — "Recovery" section
> On startup, the engine replays the WAL from the last checkpoint pointer forward, reapplying
> any mutations that were logged but not yet reflected in the checkpointed state, to recover
> from a crash.

This confirms the intended restart model: on startup, replay WAL -> reapply mutations to
reconstruct in-memory catalog state. No on-disk index/directory page exists yet (that's a later
subtask per catalog.go's "Known gap" comment), so WAL replay is the ONLY currently-implemented
reconstruction mechanism — exactly matching this subtask's "reopen catalog from disk" framing.

## Conclusion / gap identified
No existing "replay WAL into a Catalog" glue code exists anywhere in the repo. This is new,
minimal surface area (a `RecoverFromWAL` function), not just a test. Kept as small as possible:
iterates `wal.Replay` callback, decodes `RecordCatalogPut`/`RecordCatalogDelete` payloads,
calls `cat.Put`/`cat.Delete` accordingly. Placed in a new `engine/catalog/recovery.go` file
(new file, not modifying `catalog.go`/`content.go` directly, to keep the diff isolated and
easy to review/verify separately from the already-verified CRUD code).
