# Architecture Discovery

## Index-first pass
- `.cdr/index/` (task.jsnl/regression.jsonl) mentions this subtask under
  issue #41's LLD-sync track; no wal-specific index entries beyond the
  task record itself (nothing further pointed at, so proceeded to
  targeted LLD + source).
- Existing `docs/LLD/wal.md` front-matter: `last_synced_commit:
  699105baec69c1feff075a58e5ab8d2b054db317` — stale relative to current
  HEAD `75203e0`; content is scaffold-only, matches acceptance criteria's
  premise.

## Source files read (ground truth)
- `engine/wal/record.go` — `RecordType` vocabulary/constants, `String()`,
  `recordTypeSize`/`uint32LenSize`, `TypedRecord.Encode/DecodeTypedRecord`
  (with the RecordTypeInvalid/out-of-range guard), all five payload types
  (`CatalogPutPayload`, `CatalogDeletePayload`, `BTreeInsertPayload`,
  `BTreeDeletePayload`, `SplitCommitPayload`/`SplitCommitEntry`), and
  `AppendAndApply`'s fsync-before-apply contract.
- `engine/wal/writer.go` — `recordHeaderSize`=8 and its two fields;
  `segmentFilePrefix`/`segmentFileSuffix` ("wal-"/".log") naming;
  `Writer` struct + `OpenWriter` (dir creation, resuming numbering from
  the highest existing segment, `repairTornTail` before reopening for
  append); `WriteSegmentFloor`/`.segment-floor` control file and its
  monotonic-floor semantics; `Append`'s rotate-before-write rule and
  hard-error-on-oversized-record rule; `rotateLocked`; `SegmentNum`/
  `Offset` accessors; `ReadSegment`/`parseSegmentRecords`'s
  torn-tail-vs-CRC-corruption distinction.
- `engine/wal/checkpoint.go` — `manifestFileName`="manifest.json";
  `CheckpointPointer{SegmentNumber, OffsetInSegment}` JSON schema;
  `Checkpoint`'s temp-file+Sync+rename atomic write and its doc comment
  explicitly distinguishing this idiom from `engine/btree/persist.go`'s
  `SaveRoot` (weaker WriteAt+Sync, no temp file/rename); `LoadCheckpoint`
  found=false semantics; `ArchivableSegments`' `< checkpointSegmentNumber`
  boundary rule.
- `engine/wal/recovery.go` — `Replay`'s LoadCheckpoint-not-found fallback
  to (0,0); its per-segment skip/replay-from-offset loop; `readSegmentFrom`
  inclusive-start-offset behavior; `isValidRecordType` gate and its hard
  error path; the "torn tail only legal in the last segment" rule.
- `engine/wal/doc.go` — trivial one-line package doc, nothing to sync.
- Confirmed commits `4c60202` (RecordTypeInvalid/out-of-range guard) and
  `ab5e962` (checkpoint.go doc-comment correction re: SaveRoot lineage)
  exist in history and match what's currently in source.

## Discrepancies found between old scaffold and actual code
- Scaffold said "checkpoint pointer... offset up to which state has been
  durably applied" — true in spirit, but omitted that the pointer is a
  *pair* (segment number + in-segment offset), not a single global
  offset, and omitted the manifest.json/JSON-vs-binary distinction
  entirely.
- Scaffold's "Recovery" section only vaguely described replay; it did not
  mention the RecordType validation gate, the torn-tail/CRC-corruption
  distinction, or that a mid-file torn tail is only legal in the last
  segment.
- Scaffold had no mention of `RecordType` vocabulary, record header/CRC32
  format, segment naming/rotation, or the split-commit record type at
  all — these are wholly new sections.
- Scaffold's cross-reference list (catalog.md, mvcc.md, split.md,
  btree.md) is retained; mvcc.md is referenced from the original scaffold
  but no `RecordType` in current source corresponds to an MVCC
  version-pointer CAS record — left as an aspirational/forward-looking
  cross-reference per the scaffold's own framing (not verified further,
  out of scope for this wal.md-focused subtask).
