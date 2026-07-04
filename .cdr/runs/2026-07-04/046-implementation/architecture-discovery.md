# Architecture discovery — task-1.4.1

Index-first order followed: `.cdr/index/file.jsonl`/`task.jsonl` -> `docs/HLD.md` (skipped, no new
facts needed beyond LLD) -> `docs/LLD/catalog.md` -> `docs/LLD/wal.md` -> `engine/catalog/` source
-> `engine/wal/` public API.

## docs/LLD/catalog.md
- Catalog records (fileID, pathHash, currentVersion, sizeBytes, status, redirectTargetIDs,
  parentTopicID, lastModified) live in `.meta/catalog.dat`, slotted 4KB pages.
- Explicit invariant: "wal/: every catalog mutation must be logged in the WAL before being
  applied" (cross-ref to wal.md).
- No mention yet of `content/` directory layout in this doc — this subtask introduces it in code;
  LLD stays a scaffold doc (not touched, per impacted-modules scope).

## docs/LLD/wal.md
- Invariant: "every mutation to the catalog or any index ... must be logged in the WAL before it
  is applied in memory or on disk."
- `engine/wal` already implements this structurally via `AppendAndApply(w, rec, applyFn)`:
  `Writer.Append` (which fsyncs before returning) runs first; `applyFn` only runs after the
  append durably succeeds. This is the reusable idiom referenced by wal.md and used by
  `TestFsyncBeforeApply` in `engine/wal/record_test.go` (proves ordering by reading the segment
  back from disk *inside* the apply callback).

## engine/catalog/ existing source (no content.go yet — confirmed via `ls`)
- `record.go`: `CatalogRecord` struct + fixed-width `Encode()/Decode()`. `RecordEncodedSize`-byte
  layout used as the opaque blob wal's `CatalogPutPayload.Record` carries.
- `catalog.go`: `Catalog` (striped-mutex CRUD over `.meta/catalog.dat` via `FileManager`).
  `Put(rec CatalogRecord) error` is the catalog-visibility operation for this subtask — a fileID
  becomes visible via `Catalog.Get` only after `Put` returns.
- `idalloc.go`: `InvalidFileID = 0` sentinel; fileIDs from `IDAllocator.Next()` start at 1.
- `file.go`: `FileManager`, `Open(path)`, page allocation — underlying store for catalog.dat, not
  used directly by content.go (content bytes are NOT catalog pages; they are plain files under a
  `content/` directory per the acceptance criterion's literal path shape).
- Single-threaded assumption for Phase 1 still holds: no new locking is added in content.go beyond
  what `Catalog` and `wal.Writer` already provide internally (Catalog is safe for concurrent Put;
  wal.Writer's Append is internally mutex-guarded). Concurrent-safety across
  content-file-write + catalog-Put as one logical unit is explicitly out of scope (Epic 2A's job,
  matching catalog.go's own documented "known gap" precedent).

## engine/wal public API used
- `wal.NewCatalogPutRecord(fileID uint64, encodedRecord []byte) TypedRecord`
- `wal.AppendAndApply(w *wal.Writer, rec TypedRecord, apply func() error) (offset int64, err error)`
- No engine/wal changes needed or made (read-only dependency, confirmed).
