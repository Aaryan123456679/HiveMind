# Architecture Discovery — Subtask 1.3.3

## Token order followed
`.cdr/index/*` -> `.cdr/memory/*` (empty scaffold, no prior decisions recorded) ->
`docs/HLD.md`/`docs/LLD/wal.md` -> `.cdr/index/file.jsonl`, `task.jsonl`, `regression.jsonl` ->
`engine/wal/writer.go`, `engine/wal/record.go` (full read) -> `engine/wal/writer_test.go` (full
read, for existing test-style conventions).

## Key facts confirmed by reading writer.go/record.go in full

1. **`Writer.Append(payload []byte) (offset int64, err error)`** — `offset` returned is
   `w.size` (the writer's in-progress segment size) *before* the header+payload for this call
   are written, i.e. a **byte offset local to the current segment**, reset to 0 whenever
   `rotateLocked` starts a new segment. There is no global/cross-segment monotonic offset
   maintained anywhere in the package. This confirms the design guidance's assumption: the
   natural checkpoint-pointer representation is the tuple `(segmentNumber, offsetInSegment)`,
   not a single integer.

2. **Segment naming**: `wal-<N>.log` where `N` is a plain base-10 int, starting at 0,
   monotonically increasing on rotation (`segmentPath(dir, n)`, `latestSegmentNum(dir)`). Segment
   numbers are represented as plain `int` inside `Writer` (`segmentNum int`, `SegmentNum() int`).
   The subtask's suggested signature uses `uint64` for `segmentNumber` in `Checkpoint`/
   `LoadCheckpoint`/`ArchivableSegments` — segment numbers are always non-negative, so `uint64` is
   a safe, JSON-friendly widening; call sites convert with `uint64(w.SegmentNum())`.

3. **`ReadSegment(path string) ([][]byte, error)`** parses one segment file fully in memory; no
   existing helper walks a directory and returns just filenames sorted by segment number (a
   private `listSegmentFiles` test helper does this in `writer_test.go` but is test-only, not
   exported). `checkpoint.go` needs its own directory-scan helper for `ArchivableSegments`; it
   should reuse the same `wal-<N>.log` parsing convention as `latestSegmentNum` (matching prefix,
   suffix, base-10 numeric decode) rather than duplicate ad hoc logic that could disagree with
   `writer.go`'s notion of what counts as a segment file.

4. **No existing JSON-manifest precedent inside `engine/wal`.** Repo precedent for a small
   sidecar control file exists in `engine/btree/persist.go` (`.root` sidecar for the persisted
   root node ID) and `engine/catalog/idalloc.go` (`.nodealloc`-style state file) — both are raw
   binary sidecars, not JSON. `docs/LLD/wal.md` explicitly names `manifest.json` (JSON) as the
   checkpoint-tracking file, so JSON is the LLD-mandated format for this specific file, distinct
   from the binary/on-disk record format used for WAL segments themselves. This confirms the
   design guidance's choice: `encoding/json`, human-inspectable, structurally different from the
   segment binary format.

5. **Atomic-write precedent**: `engine/btree/persist.go`'s `SaveRoot` (per 1.2.6, `034-verification`
   PASS_WITH_COMMENTS) already establishes the temp-file + `Sync()` + `os.Rename` idiom for a
   small control/sidecar file in this repo. `checkpoint.go`'s manifest write follows the same
   idiom for consistency: write to `manifest.json.tmp` in the same directory (so `os.Rename` stays
   on the same filesystem, guaranteeing POSIX rename atomicity), `Sync()` the temp file before
   renaming, then `os.Rename(tmpPath, manifestPath)`.

## Decisions locked in for this subtask (relevant to 1.3.4)

- Checkpoint pointer format: JSON object `{"segment_number": uint64, "offset_in_segment": int64}`
  in `manifest.json` inside the WAL directory (same `dir` passed to `OpenWriter`).
- `LoadCheckpoint` returns `found=false, err=nil` (not an error) when `manifest.json` does not
  exist (`os.IsNotExist`), matching "fresh WAL, nothing checkpointed yet" semantics the subtask
  spec requires.
- `ArchivableSegments` lists `wal-<N>.log` files in `dir` with `N < checkpointSegmentNumber`
  strictly — the checkpoint's own segment is excluded (it may be only partially applied, and any
  segment numbered higher than the checkpoint is definitely not yet fully applied). This is an
  identification-only helper; deletion/actual archival is explicitly out of scope, deferred to a
  later subtask (not committed to a specific number here, since none is assigned in the issue's
  checklist beyond 1.3.3).
