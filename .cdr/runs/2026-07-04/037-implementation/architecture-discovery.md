# Architecture Discovery — Subtask 1.3.2

## Read order followed
1. `.cdr/memory/*` (state.md, decisions.md, impact-map.md, pending.md) — all empty/headers-only,
   no prior WAL-record-layer decisions recorded.
2. `docs/HLD.md` (system context) and `docs/LLD/wal.md` — confirms scaffold-only status, storage
   layout (`wal/wal-<segment>.log`, `manifest.json` checkpoint pointer deferred to 1.3.3), the
   core invariant ("every mutation to the catalog or any index must be logged in the WAL *before*
   it is applied in memory or on disk"), and forward references from `catalog.md`, `mvcc.md`,
   `split.md`, `btree.md` that this record layer must eventually be usable by.
3. `.cdr/index/file.jsonl` / `task.jsonl` — `task-1.3.1` verified, `engine/wal/writer.go` +
   `writer_test.go` last touched by run `2026-07-04-035-implementation`.
4. `engine/wal/writer.go` (full read) — `Writer.Append(payload []byte) (offset int64, err error)`
   already: (a) rotates segments before overflow, (b) writes an 8-byte length+CRC32 header then
   the payload, (c) calls `w.file.Sync()` before returning. This IS the fsync boundary; 1.3.2 must
   build strictly on top, never duplicate framing/rotation/fsync logic.
5. `engine/catalog/record.go` (skim) — `CatalogRecord{FileID, PathHash, CurrentVersion,
   SizeBytes, Status RecordStatus, RedirectTargetIDs []uint64, ParentTopicID, LastModified}`,
   with `Encode() ([]byte, error)` / `Decode([]byte) (CatalogRecord, error)` producing a fixed
   `RecordEncodedSize`-byte buffer. A "catalog Put" mutation is fully reconstructable from
   `FileID` + the record's own `Encode()` bytes (FileID is redundant with the encoded record's own
   FileID field, but keeping it as an explicit top-level field on the WAL record makes recovery's
   dispatch/keying trivial without decoding the whole record first). A "catalog Delete" mutation
   only needs the `FileID` being removed.
6. `engine/btree/insert.go` / `delete.go` (skim signatures) —
   `Insert(store *NodeStore, alloc *NodeAllocator, rootNodeID uint64, path string, fileID uint64)
   (newRootNodeID uint64, err error)` and
   `Delete(store *NodeStore, alloc *NodeAllocator, rootNodeID uint64, path string)
   (newRootNodeID uint64, found bool, err error)`. So a "btree Insert" mutation needs `path` +
   `fileID`; a "btree Delete" mutation needs only `path`.

## Key finding shaping the design
The acceptance criteria and test spec's exact wording ("assert WAL append completes ... prior to
a simulated apply callback firing") point at a specific, stronger-than-documentation design: the
WAL package itself should own the ordering by accepting the caller's mutation as a callback
(`apply func() error`) rather than trusting every call site to remember to call `Writer.Append`
before mutating state. This is what makes `TestFsyncBeforeApply` able to assert ordering via an
actual code path, not by reading source and hoping.

## Decision
Add `engine/wal/record.go` with:
- `RecordType byte` enum: `RecordCatalogPut`, `RecordCatalogDelete`, `RecordBTreeInsert`,
  `RecordBTreeDelete` (four kinds, directly matching the two mutation families x two operations
  each — matches the LLD's "catalog or any index" invariant wording without inventing unneeded
  generality).
- One length-prefixed payload struct per kind, each with its own `Encode()`/`Decode()` following
  `engine/catalog/record.go`'s established little-endian, explicit-offset idiom, wrapped in a
  common `TypedRecord{Type RecordType, Payload []byte}` envelope encode/decode pair (type byte +
  length-prefixed payload) that is what actually gets handed to `Writer.Append`.
- `AppendAndApply(w *Writer, rec TypedRecord, apply func() error) (offset int64, err error)`:
  encodes `rec`, calls `w.Append(...)` (which durably fsyncs per 1.3.1), and ONLY on success calls
  `apply()`. If `Writer.Append` fails, `apply` is never invoked and the error is returned
  immediately (nothing was durably logged, so nothing should be applied). If `Writer.Append`
  succeeds but `apply()` errors, `AppendAndApply` returns that error to the caller but the already
  -durable WAL offset is still meaningful: the mutation intent is safely on disk regardless of the
  in-memory apply outcome, which is correct WAL semantics — a failed apply is exactly what
  recovery replay (1.3.4) exists to retry/reconcile from the log, not a reason to have skipped
  logging in the first place.
