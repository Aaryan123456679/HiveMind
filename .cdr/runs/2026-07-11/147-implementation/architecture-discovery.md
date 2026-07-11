# Architecture Discovery — Subtask 4.5.14.4

## Index-first pass

- `docs/HLD.md` — `engine/wal/` is listed as "Write-ahead log + checkpointing
  + crash recovery" module, cross-linked to `docs/LLD/wal.md`. No
  wal-internal detail lives in the HLD; it defers entirely to the LLD.
- `.cdr/index/file.jsonl` entries for `engine/wal/*`:
  - `writer.go` tagged with `read-segment-parser`,
    `shared-torn-tail-vs-crc-corruption-parser`, last touched
    `2026-07-04-043-implementation`.
  - `recovery.go` tagged with `offset-aware-segment-reader`, same run.
  - `writer_test.go`, `recovery_test.go`, `crash_subprocess_test.go` all
    reference `ReadSegment` extensively — flags this as a widely-depended-on
    public function, not something to change signature/behavior on.
  - Downstream consumers found via grep (not in the wal index rows directly,
    but confirmed by source search): `engine/catalog/content_test.go`,
    `engine/graph/edge_append.go`, `engine/graph/edgelog.go`,
    `engine/mvcc/write_test.go` all call `wal.ReadSegment` directly.

## `docs/LLD/wal.md` (read before source)

Section "Torn-tail vs. corruption (crash-recovery discipline, task-1.3.5)"
already documents the intended end-state: *"Both `Writer`/`ReadSegment`
(`writer.go`) and `Replay` (`recovery.go`) share a single parsing routine,
`parseSegmentRecords`... 2. `ReadSegment`/`readSegmentFrom`. Both report a
torn tail via `tornTail=true`..."* — i.e. the LLD already describes the
target architecture (a single shared parser reached by both entry points).
This is the design contract this subtask must satisfy structurally, not just
behaviorally: `ReadSegment` must become a thin wrapper that reaches the
shared parser *through* `readSegmentFrom`, not merely call the same
low-level helper in parallel.

## Source read (raw, after docs/index) — confirms current state and gap

- `engine/wal/writer.go`: `ReadSegment(path) ([][]byte, error)` — did its own
  `os.ReadFile`, wrapped errors, called `parseSegmentRecords(data, 0)`
  directly, and discarded `tornTail`.
- `engine/wal/recovery.go`: `readSegmentFrom(path, startOffset)
  (records [][]byte, tornTail bool, err error)` — did the equivalent
  `os.ReadFile` (identical error-wrap string `"wal: reading segment %s:
  %w"`), plus a `startOffset` range-validation `os.ReadFile` doesn't need for
  the `ReadSegment` (offset-0) case, then called `parseSegmentRecords(data,
  int(startOffset))` (identical error-wrap string `"wal: segment %s: %w"`).
- Gap: `ReadSegment` did not call `readSegmentFrom` — it independently
  reimplemented the read+parse sequence `readSegmentFrom` already performs
  for the offset-0 case. This is exactly the duplication issue #52's subtask
  4.5.14.4 names.

## Conclusion driving the plan

Change `ReadSegment` to `records, _, err := readSegmentFrom(path, 0); return
records, err`. `parseSegmentRecords` stays exactly as-is (it's also called
directly by `writer.go`'s `repairTornTail` and by `recovery_test.go` — must
not change its signature). Update the doc comments on `ReadSegment`,
`readSegmentFrom`, and `parseSegmentRecords` to reflect the new call
relationship (`ReadSegment` -> `readSegmentFrom` -> `parseSegmentRecords`)
instead of the old parallel-call relationship, matching the LLD's already-
stated intent.
