# Requirement

Issue: #52 ("[4.5] engine/wal: additional low-severity test-coverage & doc gaps (supplement to #41)")
Subtask: 4.5.14.2 — Add exact-boundary segment-rollover test with resume across >1 segment

Source of truth: `gh issue view 52` (pulled live, not guessed):

> Acceptance criteria: A test exercises appending a record that lands exactly at
> `maxSegmentBytes`' boundary, then resuming (`OpenWriter`) with more than one
> pre-existing segment present, confirming correct segment selection/continuation.
>
> Test spec: `go test ./engine/wal/... -run TestExactBoundarySegmentRolloverResume`.
>
> Impacted modules: `engine/wal/writer_test.go`

This closes the low-severity gap recorded in `.cdr/index/regression.jsonl` (run
036-verification, subtask 1.3.1): "No dedicated test exercises the exact-boundary
case (a record whose total size lands exactly at maxSegmentBytes, correctly kept
in the current segment per code trace) or resuming with more than one
pre-existing segment file."

Scope: test-only change to `engine/wal/writer_test.go`. No production code
(`writer.go`, `recovery.go`, `checkpoint.go`) may be touched. Sibling subtask
4.5.14.1 (writer.go doc-comment fix, commit 9774bee) already landed and did not
touch writer_test.go, so this subtask starts from a clean base.
