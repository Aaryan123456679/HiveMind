# Architecture Discovery — Subtask 1.3.1

## Read order followed

1. `.cdr/memory/*` — decisions.md / pending.md / state.md / timeline.md /
   impact-map.md / regression-routes.md are all currently empty (no prior WAL
   work recorded).
2. `docs/HLD.md` — system context (engine composed of catalog, btree, graph,
   mvcc, split, rpc, wal packages; wal is the durability backbone every
   mutating module depends on).
3. `docs/LLD/wal.md` — exists but is explicitly a **scaffold only**
   (`engine/wal/doc.go` placeholder). It documents storage layout
   (`wal/wal-<segment>.log`, `manifest.json` checkpoint pointer), the
   recovery invariant (log-before-apply), and cross-references to
   catalog/mvcc/split/btree, but has no implementation detail — this
   subtask is the first real code in the package.
4. `.cdr/index/file.jsonl`, `.cdr/index/task.jsonl` — no prior `engine/wal`
   entries; last completed task is `task-1.2.6` (btree root persistence),
   commit `49f023c`.
5. `engine/catalog/file.go`, `engine/catalog/idalloc.go` — established repo
   idioms.

## Established idioms adopted from catalog package

- **Durability pattern**: `WriteAt` (or `Write`, for a growing/append-only
  file) followed by `Sync()` before returning from any mutating call
  (`FileManager.WritePage`, `IDAllocator.Next`, `persistBitmapLocked`).
  WAL's `Append` follows the same discipline: write header+payload, then
  `Sync()`, then return the offset to the caller.
- **Fixed-header binary encoding**: little-endian, `encoding/binary`,
  matching `record.go`/`page.go`/`btree/node.go`'s on-disk encoding
  convention project-wide.
- **Length-prefixed, hard-error-not-truncate on overflow**: `btree/node.go`
  returns a hard `fmt.Errorf` when encoded content doesn't fit its budget
  (`"encoded ... size %d exceeds NodeSize %d"`) rather than silently
  truncating. WAL's segment writer applies the same discipline: if a single
  record's header+payload is itself larger than `maxSegmentBytes`, `Append`
  returns a hard error (no segment could ever hold it, no silent partial
  write) instead of writing a truncated/oversized record.
- **Sidecar/derived state, no global lock beyond what's needed**: like
  `IDAllocator`, `Writer` carries its own narrow `sync.Mutex` scoped only to
  the append+rotate critical section, not a package-wide lock.
- **Package doc scaffold**: `engine/wal/doc.go` pre-exists with just the
  package clause; new code adds to the same package rather than replacing it.

## On-disk record format chosen

Each record on disk is a fixed 8-byte header followed by the raw payload:

```
[0:4]  uint32 LE  length of payload in bytes
[4:8]  uint32 LE  CRC32 (IEEE) checksum of the payload bytes
[8:8+length]      payload bytes
```

This mirrors `btree/node.go`'s length-prefixed key encoding pattern (a
uint16 length prefix there; uint32 here since WAL payloads may be much
larger than a single index key) and adds a checksum, which the issue's test
spec requires for "record integrity" assertions and which 1.3.5's future
crash-injection/torn-record detection will build on directly (a truncated
header, a truncated payload, or a header/CRC mismatch all become detectable
corruption signals for that later subtask — this subtask only needs the
format to exist and round-trip correctly).

## Segment naming / rotation convention chosen

- Segments live under `<dir>/wal-<N>.log` where `<dir>` is the caller-supplied
  WAL directory (its own name, e.g. `wal/`, is the caller's concern — matches
  the issue text "log records append to wal/wal-<segment>.log": the writer
  itself only owns the `wal-<N>.log` filename convention within whatever
  directory it's pointed at).
- `<N>` is a plain (non-zero-padded) monotonically increasing base-10
  integer, starting at 0 for a brand-new WAL directory. Documented in
  `writer.go` godoc. Zero-padding was considered but rejected: this repo's
  catalog/btree sidecar-file conventions use plain integers/binary offsets
  rather than zero-padded strings elsewhere, and a plain integer needs no
  arbitrary width choice up front.
- `OpenWriter(dir string, maxSegmentBytes int64)` creates `dir` if missing
  (`os.MkdirAll`), then scans it for existing `wal-<N>.log` files to
  determine the correct starting/resuming segment number (highest N found,
  reopened in append mode) — this is necessary even for this subtask so a
  second `OpenWriter` call against a non-empty directory does not clobber
  existing segments. Full crash-recovery / torn-tail validation of a resumed
  segment's tail is explicitly deferred to 1.3.4/1.3.5; `OpenWriter` here
  only needs correct segment-numbering continuation, not corruption
  detection.
- Rotation rule: before writing a record, if
  `currentSegmentSize + 8 + len(payload) > maxSegmentBytes` AND the current
  segment is non-empty, close the current segment and open segment N+1
  first, then write the full header+payload into the new segment. A record
  is never partially written to one segment and continued in the next. If
  a single record's encoded size alone exceeds `maxSegmentBytes` (even
  written alone into a fresh empty segment), `Append` returns a hard error
  rather than ever emitting a record split across segments or silently
  writing an oversized segment.

## Dependents

This on-disk format and rotation convention is the foundation 1.3.2
(record types + fsync-before-apply), 1.3.3 (checkpoint manifest offsets into
these segments), 1.3.4 (recovery replay), and 1.3.5 (crash-injection/torn
record detection) will all build on. Hence `impact-analysis.json` marks this
as `risk_level: medium`.
