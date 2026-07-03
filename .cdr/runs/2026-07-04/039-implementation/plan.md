# Plan — Subtask 1.3.3

1. Create `engine/wal/checkpoint.go`:
   - `type CheckpointPointer struct { SegmentNumber uint64 \`json:"segment_number"\`; OffsetInSegment int64 \`json:"offset_in_segment"\` }` — JSON-tagged, marshaled as the sole content of `manifest.json`.
   - `const manifestFileName = "manifest.json"`, `manifestTempFileName = "manifest.json.tmp"`.
   - `func Checkpoint(dir string, segmentNumber uint64, offsetInSegment int64) error` — marshal `CheckpointPointer` via `encoding/json` (with indentation for human-inspectability), write to `<dir>/manifest.json.tmp`, `Sync()` the temp file, close it, then `os.Rename(tmp, final)`.
   - `func LoadCheckpoint(dir string) (segmentNumber uint64, offsetInSegment int64, found bool, err error)` — `os.ReadFile(<dir>/manifest.json)`; if `os.IsNotExist(err)`, return `(0, 0, false, nil)`; else unmarshal and return fields with `found=true`.
   - `func ArchivableSegments(dir string, checkpointSegmentNumber uint64) ([]string, error)` — scan `dir` for `wal-<N>.log` files (reusing the same prefix/suffix/base-10-decode convention as `latestSegmentNum` in writer.go, duplicated locally since that helper is unexported and package-private-but-same-package so it CAN actually be called directly — confirm and reuse rather than duplicate), return full paths for `N < checkpointSegmentNumber`, sorted ascending by segment number.
2. Create `engine/wal/checkpoint_test.go`:
   - `TestCheckpointManifest` (the literal name required by the issue's test spec): open a `Writer` with a small `maxSegmentBytes` to force >= 2 rotations, append several typed records via `AppendAndApply`/`Append`, capture a mid-way `(segmentNumber, offsetInSegment)` pointer, call `Checkpoint`, call `LoadCheckpoint` and assert exact round-trip, call `ArchivableSegments` and assert it returns exactly the segment files strictly before the checkpoint's segment (not including the checkpoint's own segment or any later one).
   - `TestLoadCheckpointNoManifest`: fresh empty dir, `LoadCheckpoint` returns `found=false, err=nil`.
3. Self-consistency: `go build ./engine/...`, `go vet ./engine/...`, `go test ./engine/wal/... -race -v -count=1`.
4. Update `.cdr/index/file.jsonl` (two new entries) and `.cdr/index/task.jsonl` (new `task-1.3.3` entry, state `implemented`, commit SHA after commit).
5. One local commit, Problem/Solution/Impact style, no push.
6. Write `handoff.json` with pointers only.
