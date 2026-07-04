# task-1.4.3 — Content store append path: sizeBytes + threshold-check stub

## Summary

Closes out subtask 3 of 4 (GitHub issue #4, Phase 1 Storage core / content store) by
implementing the append path for on-disk content bodies: append bytes to an existing
`content/<fileID>.v1.md`, keep the catalog's `sizeBytes` accounting in sync, and expose
a threshold-check stub signal for future split logic. This subtask went through one fix
cycle before close-out: the initial implementation had a concurrency gap (a lost-update
bug under concurrent `Append` calls to the same file), which was found in verification
and fixed before this subtask was accepted. Subtask 1.4.4 (deletion/GC) remains pending,
so parent `task-1.4` stays "planned" until all four subtasks land.

## Features

- `ContentStore.Append(fileID uint64, data []byte) (...)`: appends bytes to the existing
  content body for `fileID`, byte-faithful (no transformation/normalization), journaled
  through the same WAL-before-apply durability contract used by create/write and read.
- `sizeBytes` accounting: the catalog's tracked size for a file is kept correctly in
  sync with every successful append, including under concurrent callers targeting the
  same `fileID`.
- Threshold-check stub: a signal indicating when a file's accumulated size crosses a
  configured threshold, laying groundwork for future split/rotation logic without
  implementing that logic yet.
- Per-fileID append serialization: concurrent `Append` calls to the same file are now
  correctly serialized end-to-end (read-existing/append/write-back/catalog-update),
  closing a lost-update gap identified during verification and confirmed fixed under
  a `-race` concurrency repro.

## Impact

The content store now supports create, read, and append, completing three of the four
core operations needed for the content-store epic ahead of 1.4.4 (deletion/GC). Callers
can now grow file content incrementally with correct size accounting and no risk of
silently dropped writes under concurrent append traffic to the same file, matching the
durability and correctness guarantees already established for create/write and read.

Known non-blocking follow-up (tracked in `.cdr/index/regression.jsonl`, expected to be
picked up in a small test-hardening pass or a later subtask): no test yet empirically
interleaves a concurrent `Read` with an in-flight `Append` on the same file. The
no-torn-read guarantee is sound by inspection (write-temp-then-rename plus content
write ordered before the catalog update), but is not yet pinned down by a dedicated
`-race` test.

## Verification

- Verdict: PASS_WITH_COMMENTS
- Run: 2026-07-04-055-verification

## Release Notes

Content store now supports appending to existing files with correct size tracking and
a threshold-check signal for future split logic, with concurrent appends to the same
file now safely serialized.
