# Architecture Discovery — Issue #52, subtask 4.5.14.3

## Index reads (token order: index before source)

- `.cdr/index/file.jsonl`: `engine/wal/checkpoint.go` and
  `engine/wal/checkpoint_test.go` entries confirm current feature list
  (`checkpoint-pointer`, `manifest-json-atomic-write`, `load-checkpoint`,
  `archivable-segments` / `checkpoint-manifest-roundtrip-test`,
  `archivable-segments-test`, `load-checkpoint-no-manifest-test`) —
  last touched `2026-07-04-039-implementation`, untouched since (no
  collision with 4.5.14.1's writer.go commit or the concurrent 4.5.14.2
  writer_test.go work).
- `.cdr/index/regression.jsonl`: originating entry (`040-verification`,
  subtask 1.3.3, risk=low) is the exact source of this subtask's
  acceptance criteria — quoted in requirement.md.
- `.cdr/index/task.jsonl`: prior related task 4.5.4.4 (doc-comment fix,
  commit ab5e962) and 4.5.4.5 (LLD sync, commit 7e7b97a) already resolved
  the doc-comment-lineage issue that regression.jsonl's neighboring entry
  raised; not in scope here. Confirms checkpoint.go/wal.md are currently
  accurate and stable — no drift to account for in this test-only subtask.

## HLD / LLD

- `docs/HLD.md` line 59: `wal/` = "Write-ahead log + checkpointing + crash
  recovery", pointing to `docs/LLD/wal.md`. No further HLD detail needed for
  a test-only subtask.
- `docs/LLD/wal.md` "Checkpointing (`manifest.json`)" section (read in full):
  confirms `CheckpointPointer` JSON schema (`segment_number` uint64,
  `offset_in_segment` int64), `Checkpoint(dir, segmentNumber,
  offsetInSegment) error` writes atomically via temp-file+Sync+rename,
  `LoadCheckpoint(dir) (segmentNumber uint64, offsetInSegment int64, found
  bool, err error)` returns `found=false, err=nil` on a fresh dir with no
  manifest.json. This is the exact contract the new tests must exercise;
  no LLD update needed since the doc already accurately describes the
  round-trip and overwrite behavior (only the test coverage was thin).

## Touched-file read

`engine/wal/checkpoint.go` (full file read) — `Checkpoint`/`LoadCheckpoint`
signatures confirmed as above. Notably `Checkpoint` always writes to the
same `manifestTempFileName`/`manifestFileName` (`manifest.json.tmp` /
`manifest.json`) inside `dir` and always overwrites via `os.O_TRUNC` +
`os.Rename`, so calling it twice in succession with different pointers is
expected to cleanly leave only the second pointer's values behind (no
merge, no corruption, no leftover temp file since `os.Rename` consumes it).

`engine/wal/checkpoint_test.go` (full file read) — existing tests:
`TestCheckpointManifest` (single (segment>=1, offset) pair, end-to-end
with a real `Writer`/rotation, plus `ArchivableSegments` assertions) and
`TestLoadCheckpointNoManifest` (found=false on fresh dir). Neither test
exercises `offset=0`, multiple distinct pairs table-driven, nor
double-Checkpoint overwrite — confirming the regression gap is real and
unaddressed today.

No dependency on `Writer`/rotation logic is required for the new tests:
`Checkpoint`/`LoadCheckpoint` operate purely on `dir` (any `t.TempDir()`)
and do not require an actual WAL writer or segment files to exist. This
keeps the new tests decoupled from subtask 4.5.14.2's concurrent
`writer_test.go` work (different file, and no shared test fixtures beyond
package-level identifiers already in `checkpoint.go`/`writer.go`, both
long-stable and unmodified by 4.5.14.2's scope).

## Conclusion

No production code changes needed. Purely additive tests in
`engine/wal/checkpoint_test.go`, using only the existing public
`Checkpoint`/`LoadCheckpoint` API. No LLD/HLD update needed since the docs
already accurately describe the contract being tested.
