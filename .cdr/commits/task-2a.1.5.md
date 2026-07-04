# task-2a.1.5 — Concurrent reader/writer race test, no torn reads

## Summary

Closes out the final subtask (5 of 5) under task-2a.1 / GitHub issue #6 (MVCC
content versioning epic) by adding an integration-style concurrency test that
proves the full write path (`VersionWriter.CommitVersion`, 2a.1.1/2a.1.2/2a.1.4)
and read path (Snapshot/Read, 2a.1.3) compose safely: many readers and writers
hammering the same fileID concurrently never observe a torn, partial, or mixed
version. With this subtask verified, all 5 subtasks of task-2a.1 are complete
and issue #6 is closable.

## Features

- `TestConcurrentReadersWriters` (`engine/mvcc/mvcc_test.go`, new file): spins
  up N writer goroutines committing distinct, independently-verifiable
  versions concurrently with M reader goroutines that take a snapshot and
  read, asserting every observed read matches exactly one committed payload
  via a precomputed valid-content set. Real goroutines and `sync.WaitGroup`
  are used to force genuine overlap, not simulated interleaving.
- Reuses existing `write_test.go` test helpers (`newTestCatalog`,
  `newTestWAL`, `countVersionFiles`) rather than duplicating fixture setup,
  keeping the test integration-focused across write.go + read.go instead of
  re-unit-testing either in isolation.
- Test command: `go test ./engine/mvcc/... -run TestConcurrentReadersWriters -race`.

## Impact

Provides the capstone correctness guarantee for MVCC content versioning: the
atomic temp-file-plus-rename publish combined with the version-pointer CAS
(now WAL-safe per 2a.1.4) means concurrent readers can only ever see the old
file, ENOENT, or a fully-formed new file — never a partial one — and this
test exercises that invariant under real concurrent load rather than merely
asserting it exists on paper. This is the last piece of test coverage needed
to close GitHub issue #6.

## Verification

- verdict: PASS_WITH_COMMENTS
- run_id: 2026-07-04-079-verification

Verification confirmed the test is structurally airtight against false
positives: given the atomic temp-file+rename publish and WAL-before-apply
CAS, a valid-set miss can only indicate a genuine torn-read/lost-update bug,
not test flakiness. Confirmed genuine concurrency (real goroutines/
WaitGroups, true overlap), exact map-key content equality assertion, and a
coherent self-reported bugfix (orphaned-file tolerance). Ran clean at
`-race -count=15` with zero flakes; full `engine` module regression-clean
across engine/btree/catalog/mvcc/wal. Non-blocking comment: the
`CurrentVersion == 0` edge case is only exercised via natural goroutine
scheduling, not deterministically forced — flagged for a possible future
hardening pass, not a blocker.

## Release Notes

Added a race-tested concurrency proof for MVCC content versioning: readers
and writers can safely operate on the same file concurrently with no risk of
observing torn or partial versions. This completes the MVCC content-
versioning epic (GitHub issue #6) — all 5 subtasks (version writer, CAS
commit, snapshot read, WAL-integrated CAS, and this concurrency test) are
now implemented and verified.
