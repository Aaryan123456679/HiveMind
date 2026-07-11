# Requirement — Subtask 4.5.2.2 (issue #39)

Correct `TestGCUnderConcurrency`'s doc comment (engine/mvcc/gc_test.go, ~lines
515-529) so it no longer claims equivalence to
`TestNewSnapshotClosesEpochAcquireVersionReadRace`'s specific epoch-acquire-
before-version-read TOCTOU. The comment should instead state this test is a
complementary broad-stress test for general premature-reclaim bugs, not a
substitute for the narrow deterministic regression test. Optionally widen
reader-goroutine overlap (loop on a shared stop channel instead of a fixed
round count) so the "throughout the test duration" framing is accurate.

Test spec: `go test ./engine/mvcc/... -race -run TestGCUnderConcurrency` must
still pass; doc-comment diff reviewed for accuracy — no new test required
beyond confirming the existing suite is green.

Impacted modules: `engine/mvcc/gc_test.go` only.
