# Requirement — Subtask 4.5.14.4

Source: `gh issue view 52` (issue #52, "[4.5] engine/wal: additional low-severity
test-coverage & doc gaps (supplement to #41)"), verbatim subtask text.

## Subtask 4.5.14.4

> Refactor `ReadSegment` (`writer.go`) to delegate to `readSegmentFrom(path, 0)`
> (`recovery.go`), eliminating the duplicated CRC-check/parsing logic between
> the two.

- **Acceptance criteria**: `ReadSegment` (writer.go) delegates to
  `readSegmentFrom(path, 0)` (recovery.go), eliminating the
  line-for-line-duplicated CRC-check/parsing logic between the two, both
  sharing a single extracted parsing helper.
- **Test spec**: `go test ./engine/wal/... -race` green post-refactor (no new
  test name specified — this subtask is a pure internal refactor of existing,
  already-tested code paths).
- **Impacted modules**: `engine/wal/writer.go`, `engine/wal/recovery.go`.
- Each subtask in issue #52 is sized to exactly one commit.

## Confirmation against actual source (pre-refactor state)

Read `engine/wal/writer.go` and `engine/wal/recovery.go` in full before making
any change. Finding: a shared low-level parser (`parseSegmentRecords`,
`writer.go`) already existed and was already called by both `ReadSegment`
(`writer.go`, at offset 0) and `readSegmentFrom` (`recovery.go`, at an
arbitrary offset) — so the CRC-check/parsing *logic itself* was not
line-for-line duplicated at that granularity. The actual remaining
duplication was one level up: `ReadSegment` had its own `os.ReadFile` +
error-wrap + `parseSegmentRecords(data, 0)` call, structurally identical to
`readSegmentFrom`'s `os.ReadFile` + error-wrap + `parseSegmentRecords(data,
startOffset)` call, just with `startOffset` hardcoded to 0 and without the
`tornTail` return value being surfaced.

This matches the issue's literal, actionable instruction ("`ReadSegment`
delegates to `readSegmentFrom(path, 0)`") even though the "line-for-line CRC
check" framing was already partially addressed by a prior subtask. No scope
adjustment was needed beyond implementing the literal delegation the issue
asks for: it directly collapses the last remaining bit of duplicated
read-then-parse boilerplate between the two functions. Behavior for all
existing callers of `ReadSegment` is unchanged (same signature, same error
wrapping text, same torn-tail/CRC-corruption semantics).
