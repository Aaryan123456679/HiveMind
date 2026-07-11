# Requirement — Issue #52, subtask 4.5.14.3

Source: `gh issue view 52` (verbatim body, subtask 4.5.14.3 entry) plus the two
regression.jsonl entries (`040-verification`, subtask 1.3.3, risk=low) that
originated this subtask.

## Acceptance criteria (verbatim intent)

> table-driven Checkpoint round-trip test across offset/segment pairs plus
> double-Checkpoint-overwrite test — `checkpoint_test.go`'s round-trip
> coverage exercises multiple distinct `(segment, offset)` pairs (including
> `offset=0`); a test confirms calling `Checkpoint` twice in succession
> cleanly overwrites `manifest.json` with no corruption.

## Test spec (verbatim)

```
go test ./engine/wal/... -run TestCheckpointManifestRoundTripTableDriven
go test ./engine/wal/... -run TestCheckpointDoubleOverwrite
```

## Impacted modules

- `engine/wal/checkpoint_test.go` (only file to change)

## Originating regression note (2026-07-04, 040-verification, subtask 1.3.3, risk=low)

"Round-trip fidelity (Checkpoint then LoadCheckpoint) is only empirically
exercised for one (segmentNumber, offsetInSegment) pair across the whole test
suite, and no test covers calling Checkpoint twice in succession to confirm a
clean overwrite rather than corruption of manifest.json."

Recommendation: "Add a table-driven round-trip test across several distinct
pairs (including offset=0, a large segment number) plus a
double-Checkpoint-overwrite test."

## Scope boundary

- Test-only change, additive. Do not modify `engine/wal/checkpoint.go`,
  `writer.go`, `recovery.go`, or any other file.
- Two new test functions required, with the exact names in the test spec:
  `TestCheckpointManifestRoundTripTableDriven` and
  `TestCheckpointDoubleOverwrite`.
- Existing `TestCheckpointManifest` and `TestLoadCheckpointNoManifest` must
  remain intact and passing (no regressions).
