---
last_synced_commit: 75203e02d91f23b8146ff23976a95d9beab5baa7
---

# LLD: `engine/wal/`

Status: implemented (`engine/wal/*.go`). See [HLD.md](../HLD.md) for system
context.

## Purpose

Write-ahead log providing durability and crash recovery for all catalog/index
mutations across the engine. Every mutation is encoded as a typed record,
durably appended (with fsync) to an append-only segment file, and only then
applied to in-memory/on-disk state (see [Invariant](#invariant) below).

## Storage layout

### Segment files and naming

- Append-only segment files live in a single WAL directory (the `dir`
  argument to `OpenWriter`), named `wal-<N>.log`, where `N` is a plain
  (not zero-padded), monotonically increasing base-10 integer starting at 0
  for a brand-new WAL directory.
- `OpenWriter` resumes numbering from the highest-numbered existing segment
  file rather than starting over or overwriting existing data. A resumed
  segment is reopened in append mode with its current size restored.
- A `.segment-floor` control file (`WriteSegmentFloor`/`readSegmentFloor`)
  records a monotonic minimum segment number to use once a directory's
  existing segment files have all been removed by some other caller (e.g.
  `engine/graph/edgelog.go`'s `TruncateNode`). This exists so that segment
  numbering never restarts at 0 and accidentally reuses a number some other
  durable record elsewhere (e.g. a graph compaction sidecar) already treats
  as "already accounted for." `WriteSegmentFloor` itself uses the same
  temp-file+`Sync`+`os.Rename` atomic-write idiom as `checkpoint.go`'s
  `Checkpoint` (see [Checkpointing](#checkpointing-manifestjson)), and never
  regresses an already-published floor.

### Record header and rotation

- Every record is prefixed with a fixed 8-byte header
  (`recordHeaderSize` in `writer.go`):
  - `[0:4]` — uint32, little-endian, length of the payload in bytes.
  - `[4:8]` — uint32, little-endian, CRC32 (IEEE) checksum of the payload
    bytes.
- `Writer.Append` writes the header, then the payload, then calls
  `file.Sync()` before returning — every `Append` call is durable (fsynced)
  by the time it returns.
- Segments are size-rotated: `OpenWriter` takes a `maxSegmentBytes` and
  `Writer.Append` rotates to a new segment file (`rotateLocked`, opening
  `wal-<segmentNum+1>.log`) *before* writing if the incoming record would
  overflow the current segment. A record is never split or partially
  written across two segments.
- A single record whose total encoded size (header + payload) exceeds
  `maxSegmentBytes` is rejected outright with a hard error and nothing is
  written — the package refuses to silently truncate or split it, matching
  the repo's "hard-error, not truncate, on overflow" convention used
  elsewhere (e.g. `engine/btree/node.go`'s encode functions).

## Record types

`RecordType` (a `byte`) identifies which kind of mutation a `TypedRecord`'s
payload encodes (`engine/wal/record.go`). A `TypedRecord` on disk is simply a
1-byte type tag followed by the type's kind-specific payload bytes (no
separate length prefix — the record header above already carries payload
length and CRC32).

| Constant | Value | Payload | Encoding |
| --- | --- | --- | --- |
| `RecordTypeInvalid` | 0 | — | Reserved zero value; never a valid on-disk record type. |
| `RecordCatalogPut` | 1 | `CatalogPutPayload{FileID uint64, Record []byte}` | 8-byte LE `FileID`, then `Record` as a uint32-length-prefixed blob (already-encoded `catalog.CatalogRecord` bytes, treated opaquely by this package). |
| `RecordCatalogDelete` | 2 | `CatalogDeletePayload{FileID uint64}` | 8-byte LE `FileID`, fixed width, no length prefix. |
| `RecordBTreeInsert` | 3 | `BTreeInsertPayload{Path string, FileID uint64}` | uint32-length-prefixed `Path`, then 8-byte LE `FileID`. |
| `RecordBTreeDelete` | 4 | `BTreeDeletePayload{Path string}` | uint32-length-prefixed `Path` only. |
| `RecordSplitCommit` | 5 | `SplitCommitPayload{OriginalFileID uint64, OldPath string, EncodedCatalogRecord []byte, Entries []SplitCommitEntry}` | 8-byte LE `OriginalFileID`; length-prefixed `OldPath`; length-prefixed `EncodedCatalogRecord` (the full, final post-split `catalog.CatalogRecord`, opaque bytes); 4-byte LE entry count; then, per entry, length-prefixed `NewPath` + 8-byte LE `FileID` + 8-byte LE `SizeBytes`. Describes one atomic split-commit transaction in full (`engine/split`'s `ExecuteSplitAtomic`), so `RecoverSplitCommits` can redo the whole catalog/B+Tree/graph effect from a single record. |

`RecordType.String()` renders each of the above by name for error messages,
and falls back to `RecordType(<n>)` for any unrecognized byte value.

`DecodeTypedRecord` (the inverse of `TypedRecord.Encode`) validates the type
tag: it rejects both `RecordTypeInvalid` (0) and any byte greater than
`RecordSplitCommit` (currently 5) with a hard decode error, rather than
silently accepting an out-of-range value and only failing later at whatever
payload-specific decode call happens to run next. This guard was added in
subtask 4.5.4.3 (commit `4c60202`) to close a gap where an unset/garbage
`RecordType` byte could otherwise be decoded as if valid.

`AppendAndApply` (`record.go`) is the package's fsync-before-apply write
path: it encodes a `TypedRecord`, durably appends it via `Writer.Append`
(which does not return until fsynced), and only then invokes the
caller-supplied `apply` callback. If encoding or `Append` fails, `apply` is
never called. If `Append` succeeds but `apply` fails, `AppendAndApply` still
returns the valid offset alongside a wrapped error — the WAL record is
already durable at that point regardless of whether the in-memory/on-disk
apply step succeeded; a failed apply is expected to be reconciled by
`Replay` on next startup, not rolled back.

## Checkpointing (`manifest.json`)

- `CheckpointPointer` is the on-disk checkpoint schema, JSON-encoded (not
  this package's binary record format — the LLD deliberately calls for a
  small, human-inspectable control file, distinct in spirit and format from
  the append-only binary segments it points into):

  ```json
  {
    "segment_number": 0,
    "offset_in_segment": 0
  }
  ```

  - `segment_number` (`uint64`, JSON key `segment_number`): the WAL segment
    number the checkpoint refers to.
  - `offset_in_segment` (`int64`, JSON key `offset_in_segment`): the
    per-segment byte offset (as returned by `Writer.Offset()`) within that
    segment, up to which state has been durably applied.
  - This is a tuple, not a single global/monotonic offset, because
    `Writer.Append`'s returned offset is itself per-segment and resets to 0
    on every rotation; a checkpoint must carry both which segment and where
    within it to be meaningful across rotation.
- `Checkpoint(dir, segmentNumber, offsetInSegment)` writes this pointer to
  `manifest.json` inside the WAL directory (the same `dir` passed to
  `OpenWriter`), atomically: the encoded JSON is written to a temp file
  (`manifest.json.tmp`) in the same directory, `fsync`ed, closed, then moved
  into place via `os.Rename` (atomic on POSIX filesystems since the temp
  file and final path share a directory/filesystem). This avoids ever
  leaving a torn or partially-written `manifest.json` on disk, even across a
  mid-write crash.
  - This temp-file+`Sync`+`os.Rename` idiom is new to this codebase — it is
    **not** modeled on `engine/btree/persist.go`'s `SaveRoot`, which
    durably persists its root node ID via a structurally weaker technique
    (an in-place `f.WriteAt` followed by `f.Sync`, with no temp file and no
    rename). The two are different atomic-write strategies; `checkpoint.go`
    should not be read as mirroring `SaveRoot`'s precedent. (This doc-comment
    lineage was corrected in subtask 4.5.4.4, commit `ab5e962`, after an
    earlier draft mistakenly implied the two were the same idiom.)
- `LoadCheckpoint(dir)` reads `manifest.json` back. If it does not exist yet
  (a fresh WAL with nothing checkpointed), it returns `found=false` with a
  nil error — an expected, non-error state, not a failure.
- `ArchivableSegments(dir, checkpointSegmentNumber)` returns the paths of
  `wal-<N>.log` segment files whose segment number `N` is **strictly less
  than** `checkpointSegmentNumber`, sorted ascending. The checkpoint's own
  segment (`N == checkpointSegmentNumber`) is deliberately excluded — the
  checkpoint offset may land partway through that segment, so it is not yet
  fully durably-applied-and-safe-to-archive as a whole — and any segment
  numbered higher than the checkpoint is newer and therefore also not
  eligible. This function only identifies eligible segments; it does not
  delete, truncate, or otherwise archive them itself (actual
  archival/deletion is a separate, not-yet-implemented concern).

## Recovery (`Replay`)

`Replay(dir, apply)` is the package's recovery entrypoint: on startup, a
caller invokes it to reapply every mutation durably logged to the WAL but
not yet reflected in checkpointed state, in on-disk order, exactly once,
before resuming normal operation (e.g. before calling `OpenWriter` again to
resume appending).

- **Checkpoint resolution.** `Replay` reads the directory's checkpoint
  pointer via `LoadCheckpoint`. If none exists (`found=false` — a fresh
  WAL), it starts from segment 0, offset 0.
- **`OffsetInSegment` is an inclusive-start convention.** Within the
  checkpoint's own segment, every record whose start is strictly before
  `OffsetInSegment` is skipped (already durably applied prior to the
  checkpoint); replay resumes reading **at** `OffsetInSegment` itself, so a
  record that starts exactly at that offset is replayed, not skipped. Every
  subsequent segment (any segment number greater than the checkpoint's) is
  replayed in full, in ascending segment-number order.
- **RecordType validation gate.** For each record read during replay,
  `Replay` decodes it via `DecodeTypedRecord` and then additionally checks
  `isValidRecordType`, which accepts only `RecordCatalogPut`,
  `RecordCatalogDelete`, `RecordBTreeInsert`, `RecordBTreeDelete`, and
  `RecordSplitCommit` — rejecting `RecordTypeInvalid` and any unrecognized
  type with a hard error rather than silently skipping or succeeding. Only
  a record that passes this gate is handed to the caller-supplied `apply`
  callback, in order, exactly once. If `apply` is `nil`, `Replay` still
  performs this validation but skips the callback (useful as a dry-run
  integrity check).
- `Replay` does not itself decode a `TypedRecord`'s payload into a concrete
  `engine/catalog`/`engine/btree` mutation and apply it to a live store —
  that wiring is the responsibility of the caller-supplied `apply`
  callback, mirroring `record.go`'s `AppendAndApply`.
- If the checkpoint pointer already covers everything durably written (its
  segment is the last existing segment and its offset already equals that
  segment's current size), no segment yields any records at or past that
  point: `apply` is never invoked and `Replay` returns `nil`. This falls out
  of the same general loop as the non-empty case, not a special-cased
  no-op.

### Torn-tail vs. corruption (crash-recovery discipline, task-1.3.5)

Both `Writer`/`ReadSegment` (`writer.go`) and `Replay` (`recovery.go`) share
a single parsing routine, `parseSegmentRecords`, that draws a hard line
between two failure modes when reading a segment:

- **Torn tail** — a truncated header or a truncated payload at the very end
  of the file. This is exactly what a crash mid-`Append` produces (`Append`
  only ever writes header then payload, in that order, with nothing else
  appended until the next record), so it is treated as an incomplete write,
  **not an error**: parsing stops cleanly and every record parsed so far is
  returned with a nil error.
- **CRC mismatch on a full-length record** — a different failure mode,
  because a crash mid-write can never produce a full-length record with
  flipped bits (the crash always leaves the record short, never
  full-length-but-corrupted). This is treated as genuine bit-level
  corruption and is always a hard error, never silently discarded.

This distinction is applied consistently in three places:

1. **`OpenWriter` resuming a segment.** Before reopening a resumed segment
   for append, `repairTornTail` validates its tail using the same
   `parseSegmentRecords` rule. If a torn tail is found, `OpenWriter`
   physically truncates the file to the last valid record boundary before
   reopening it — so a resumed `Writer`'s on-disk state and a fresh
   `Replay`'s view of the same directory always agree on where "the last
   valid record" ends. A CRC mismatch on a full-length record, by contrast,
   is *not* silently discarded here: `OpenWriter` fails closed with a clear
   error rather than resuming (and therefore appending) onto a segment
   already known to be corrupt.
2. **`ReadSegment`/`readSegmentFrom`.** Both report a torn tail via
   `tornTail=true` with a nil error and return every record parsed strictly
   before the torn bytes; a CRC mismatch is returned as a hard `err`,
   alongside the records parsed strictly before the corrupt one.
3. **`Replay`'s last-segment rule.** A torn tail can only legitimately arise
   in the segment a crashed process was actively writing to at the moment
   of the crash — necessarily the highest-numbered (last) segment. If
   `Replay` encounters a torn tail in any segment that is *not* the last
   segment, it treats this as a hard on-disk-inconsistency error rather
   than silently discarding it like a genuine torn tail. When the torn tail
   *is* in the last segment (the expected case), `Replay` still applies
   every record parsed strictly before it, in order, then stops — it does
   not raise this as an error. Similarly, a CRC-corruption `readErr`
   encountered partway through a segment during replay does not discard
   records parsed before the corrupt one: those are applied first, and the
   error is only surfaced afterward so the caller knows replay stopped
   early.

## Invariant

Every mutation to the catalog or any index (B+Tree, graph adjacency) must be
logged in the WAL *before* it is applied in memory or on disk (enforced
structurally by `AppendAndApply`, not just by convention). This is the
durability backbone other modules depend on:

- [catalog.md](catalog.md) — record mutations
- [mvcc.md](mvcc.md) — version-pointer CAS
- [split.md](split.md) — entire multi-step split sequence commits as a
  single WAL-covered, fsynced transaction (`RecordSplitCommit`)
- [btree.md](btree.md) — index insert/delete

## Known risks

- Segment archival/deletion itself is out of scope for this package:
  `ArchivableSegments` only identifies eligible segments; nothing in
  `engine/wal` currently deletes or truncates them. Callers that do so
  (e.g. `engine/graph`'s edge-log truncation) are responsible for using
  `WriteSegmentFloor` correctly to avoid segment-number reuse.
- Beyond the general requirement that recovery correctness be tested under
  crash-injection scenarios (tracked under the engine's `-race` testing
  convention in [AGENT.md](../../AGENT.md) for concurrent-write paths that
  feed the log — see `engine/wal/crash_subprocess_test.go`), there is no
  risk unique to this module beyond what's documented above.

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md), [mvcc.md](mvcc.md), [split.md](split.md), [btree.md](btree.md)
