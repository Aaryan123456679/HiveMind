# Architecture discovery — Subtask 1.3.4

## Read order (per protocol)
1. `.cdr/memory/*` — all empty (state.md, pending.md, impact-map.md, decisions.md
   contain only headers; no prior notes to carry forward beyond what's already
   captured in the indexes).
2. `.cdr/index/task.jsonl` — confirms 1.3.1, 1.3.2, 1.3.3 all `verified`, with
   commits 1a12643c, 4e418a1, c1c89f7 respectively.
3. `.cdr/index/regression.jsonl` — carried forward two live findings:
   (a) 1.3.2 `RecordTypeInvalid` decode-validation gap (medium/low severity,
   explicitly earmarked for 1.3.4 closure) — addressed by this subtask.
   (b) 1.3.3 confirms `CheckpointPointer{SegmentNumber, OffsetInSegment}` is
   correct and round-trips; no action needed beyond consuming it as designed.
   (c) 1.3.1 flagged `OpenWriter`'s resume path does not validate/tolerate a
   torn tail — explicitly deferred to 1.3.4/1.3.5. Read `writer.go` in full to
   confirm: `OpenWriter`'s own resume logic (reopening the highest-numbered
   segment in append mode) is UNCHANGED by this subtask. This subtask adds a
   separate, explicit `Replay` entrypoint a caller invokes on startup BEFORE
   resuming writes; it does not modify `OpenWriter` itself. Torn-tail
   *validation* (detecting and truncating a partial trailing record so the
   writer can safely resume appending past it) is explicitly 1.3.5's
   crash-injection scope, not this subtask's — `ReadSegment`/the new
   offset-aware reader here already hard-error on a truncated trailing
   record (truncated header or truncated payload), which is the correct
   behavior for *this* subtask (a well-formed pre-populated WAL with no
   injected crash/torn record, per the test spec). Scope boundary recorded
   below.
4. `.cdr/index/file.jsonl` — lists exact feature tags / last-change-run per
   file; used to know which files are "already verified" (writer.go,
   record.go, checkpoint.go, writer_test.go, record_test.go, checkpoint_test.go)
   vs. new (recovery.go, recovery_test.go).
5. `docs/LLD/wal.md` — confirmed scaffold-only/stale (as flagged across
   1.3.1-1.3.3's verifications: prose fragments with words dropped, e.g. "On
   startup, engine replays WAL last checkpoint pointer forward" -- missing
   "from the"). Not touched by this subtask (three prior verifications have
   already logged this as a compounding, pre-existing gap; an LLD-sync pass
   is out of scope for an implementation subtask targeting only
   recovery.go/recovery_test.go per the issue's stated impacted modules).
6. Read `engine/wal/writer.go`, `engine/wal/record.go`, `engine/wal/checkpoint.go`
   IN FULL (verified, done above).

## Key facts extracted from source

- `Writer.Append(payload []byte) (offset int64, err error)`: offset returned
  is the BYTE OFFSET WITHIN THE SEGMENT where that record's header begins,
  before the header+payload were written. `CheckpointPointer.OffsetInSegment`
  is defined (checkpoint.go doc comment) to be exactly one of these
  per-record offsets — i.e. always record-aligned. This is what allows a
  recovery reader to treat `OffsetInSegment` as a safe starting byte
  position at which to begin parsing records with no partial-record
  boundary concern (as long as the WAL itself is not torn, which is this
  subtask's assumption per its own test spec — torn/crash handling is
  1.3.5).
- `ReadSegment(path) ([][]byte, error)` parses ONE segment file IN FULL from
  offset 0, returning each record's PAYLOAD (i.e., the bytes previously
  passed to `Writer.Append` — for this package's own usage that is always a
  `TypedRecord.Encode()` blob: 1-byte type tag + kind-specific payload). It
  does not accept a starting offset. Per design guidance, since this subtask
  needs to skip forward to a specific offset within the checkpoint's own
  segment, a new offset-aware reader is needed. Decision: implement a
  package-private `readSegmentFrom(path string, startOffset int64) ([][]byte, error)`
  local to recovery.go that mirrors `ReadSegment`'s exact parsing loop
  (header/payload/CRC validation, identical error strings/shape) but begins
  the parse loop at `off := startOffset` instead of `off := 0`. This is a
  small (~15 line), deliberate, LOCAL duplication rather than refactoring
  `ReadSegment` itself, because: (a) the issue's impacted-modules list is
  scoped to `recovery.go`/`recovery_test.go` only; (b) `writer.go` is already
  verified (1.3.1) and touching it re-opens verification surface for no
  functional gain (ReadSegment's full-file behavior is still independently
  useful/tested and must not change); (c) the duplicated logic is small,
  stable (shares the same `recordHeaderSize`/`offRecordLength`/`offRecordCRC`
  constants already defined in writer.go, so there is no drift risk on the
  on-disk format itself, only the loop's starting offset differs).
- Segment listing: `latestSegmentNum` (writer.go, unexported) and
  `ArchivableSegments` (checkpoint.go, exported) each independently scan
  `dir` for `wal-<N>.log` files. Decision: add one more package-private
  scanning helper `listSegmentNumbers(dir) ([]uint64, error)` in recovery.go
  returning ALL segment numbers found, sorted ascending (no filtering) --
  this is a third, minimal, deliberately-local instance of the same scan
  pattern for the same "don't touch already-verified files" reasoning above,
  documented here per the run's mandate to record impact-analysis
  reasoning explicitly.
- `TypedRecord`/`DecodeTypedRecord` (record.go): `DecodeTypedRecord` does
  NOT validate `Type`. `RecordType` has exactly 4 valid non-zero values:
  `RecordCatalogPut=1`, `RecordCatalogDelete=2`, `RecordBTreeInsert=3`,
  `RecordBTreeDelete=4`. `RecordTypeInvalid=0` is reserved/never valid.
  Decision: add validation INSIDE `Replay` (recovery.go), at the dispatch
  point, per the design guidance's explicit instruction ("closing the
  flagged 1.3.2 gap ... don't silently skip or silently succeed") — a
  `isValidRecordType(t RecordType) bool` helper local to recovery.go, called
  immediately after `DecodeTypedRecord` succeeds and before `apply` is
  invoked. `record.go` itself is left unmodified (scope stays inside the
  issue's stated impacted modules); the gap is closed at the one call site
  (recovery replay) the regression note names as the required closure point.
- `CheckpointPointer{SegmentNumber uint64, OffsetInSegment int64}` /
  `LoadCheckpoint(dir) (segmentNumber uint64, offsetInSegment int64, found bool, err error)`:
  `found=false` with nil error is the documented "fresh WAL, nothing
  checkpointed" case — `Replay` must treat this as "start from segment 0,
  offset 0" per design guidance.

## Scope boundary (explicitly confirmed, per design guidance's request)

`Replay`'s `apply func(TypedRecord) error` callback is a caller-supplied
hook; THIS subtask does not wire `Replay` into `engine/catalog` or
`engine/btree` itself (i.e., there is no real "decode payload and mutate a
live B+Tree/catalog" implementation here). That real wiring belongs to a
later phase (Phase 2a's catalog/btree integration is the natural place,
per `docs/LLD/wal.md`'s own cross-references to catalog.md/btree.md as
downstream consumers of this durability backbone) — consistent with how
1.3.2's `AppendAndApply` also took an opaque `apply func() error` without
this package importing `engine/catalog`/`engine/btree` (record.go's
`CatalogPutPayload` doc comment: "This package deliberately does not
import engine/catalog... decoding those bytes back into a
catalog.CatalogRecord is the recovery layer's (1.3.4's) job" — read
literally, this could be construed as asking 1.3.4 to decode into concrete
catalog/btree types; however, since `engine/catalog`/`engine/btree` provide
no live in-memory store construction API surface this package could
plausibly hold onto across a `Replay` call today, and since the issue's own
impacted-modules list and test spec both describe a caller-supplied
`apply` callback pattern mirroring 1.3.2's `AppendAndApply`, this
subtask implements `Replay` generically over `TypedRecord` and defers the
concrete decode-and-mutate wiring to the real catalog/btree integration
task. This is a scope call made explicit here per the design guidance's
request to "confirm this scope boundary makes sense and document it.").
For THIS subtask's own test (`TestRecoveryReplay`), a test-local fake
`apply` callback recording the sequence of `TypedRecord`s it was called
with is used, mirroring 1.3.2's `TestFsyncBeforeApply` injected-callback
pattern (record_test.go).

## No-op detection design

Design guidance suggests explicitly comparing the checkpoint pointer
against `os.Stat` of the last segment's size to detect the "checkpoint
already covers everything" case. On reflection, this subtask instead lets
the no-op fall out NATURALLY from the general replay loop: reading a
segment "from offset X" where X already equals that segment's on-disk size
returns zero records (the parse loop `for off < len(data)` never executes
its body). So when the checkpoint pointer is at the true end of the last
segment, `readSegmentFrom` on that segment returns an empty slice, `apply`
is never called across the whole `Replay` call, and no special-cased
early-return branch is needed — this is simpler and less bug-prone than a
separate `os.Stat`-based fast path, while still satisfying the acceptance
criterion (`apply` never invoked) and the test spec (assert no-op). The one
explicit `os.Stat`-adjacent check kept is a guard for `checkpoint segment
number > highest existing segment number` (an inconsistent/impossible state
under normal operation, since `ArchivableSegments` always excludes the
checkpoint's own segment from archival) — this returns a hard error rather
than silently no-op'ing, since it would indicate on-disk corruption or a
caller bug (checkpointing beyond what was actually written).
