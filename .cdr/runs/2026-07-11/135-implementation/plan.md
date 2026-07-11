# Plan — Issue #52, subtask 4.5.14.3

## New test 1: `TestCheckpointManifestRoundTripTableDriven`

Table-driven over a slice of `(segmentNumber uint64, offsetInSegment
int64)` pairs, run as subtests (`t.Run(name, ...)`), each in its own fresh
`t.TempDir()` (isolation, no shared state across table entries):

- `{segment: 0, offset: 0}` — the explicit `offset=0` case required by the
  acceptance criteria, and also segment 0 (the "nothing rotated yet" case).
- `{segment: 0, offset: 128}` — offset=0 segment but nonzero offset within
  it.
- `{segment: 1, offset: 0}` — nonzero segment, offset=0 (start of a
  rotated segment).
- `{segment: 5, offset: 4096}` — a mid-range pair.
- `{segment: ^uint64(0)>>1 or a large but valid uint64, offset:
  large int64}` — a "large segment number" boundary case per the
  regression note's explicit recommendation ("a large segment number").
  Use a large but realistic value (e.g. `math.MaxUint32` for segment,
  `math.MaxInt32` for offset) rather than the true uint64/int64 max, since
  the schema is JSON-numeric and unrealistic extreme values are not the
  point of this test.

For each pair: call `Checkpoint(dir, seg, off)`, then `LoadCheckpoint(dir)`,
assert `found == true`, `err == nil`, and the round-tripped
`(segmentNumber, offsetInSegment)` exactly match the input pair.

## New test 2: `TestCheckpointDoubleOverwrite`

Single fresh `t.TempDir()`. Call `Checkpoint(dir, segA, offA)` with one
pointer, then `Checkpoint(dir, segB, offB)` with a second, different
pointer (segA != segB and offA != offB, to make sure a stale-merge bug
would be caught). Then:

- `LoadCheckpoint(dir)` must return the **second** pointer's values
  exactly (not the first, not some merge).
- Additionally read `manifest.json` directly (`os.ReadFile` +
  `json.Unmarshal` into a local anonymous/`CheckpointPointer`-shaped
  struct, or just decode via the package's own `CheckpointPointer` type
  since the test is in-package) to confirm the file is valid, single,
  well-formed JSON with no leftover trailing content/corruption (e.g. no
  concatenation of both writes) — read the file bytes and independently
  `json.Unmarshal` them, asserting no error and the decoded value matches
  the second pointer.
- Confirm no leftover `manifest.json.tmp` temp file remains after the
  second `Checkpoint` call (`os.Stat` on the temp path should return
  `os.IsNotExist(err) == true`), since `os.Rename` should have consumed it
  both times.

## Naming / style consistency

Follow the existing file's conventions: package `wal`, `t.Fatalf` for
setup/precondition failures, `t.Errorf` for value-mismatch assertions,
doc comments on each `Test...` function describing intent and referencing
the regression source, consistent with `TestCheckpointManifest`'s and
`TestLoadCheckpointNoManifest`'s existing doc-comment style.

## No production code changes.

## Self-consistency checks planned

- `gofmt -l engine/wal/checkpoint_test.go` clean.
- `go vet ./engine/wal/...` clean.
- `go test ./engine/wal/... -run TestCheckpointManifestRoundTripTableDriven -v`
  passes.
- `go test ./engine/wal/... -run TestCheckpointDoubleOverwrite -v` passes.
- `go test ./engine/wal/... -race` full package green (no regression to
  existing tests).
