# task-1.4.2 — Content store layout: content/<fileID>.v1.md full-file read path

## Summary
Closes out subtask 2 of 4 (GitHub issue #4, Phase 1 Storage core / content store) by
implementing the full-file read path for on-disk content bodies: given a catalog
`fileID`, resolve and read back the exact bytes of `content/<fileID>.v1.md` written by
the create/write path from task-1.4.1. This is a pure read (no WAL involvement, no
mutation) that completes the create-then-read round trip for the content store.
Subtasks 1.4.3-1.4.4 (update/versioning, deletion/GC) remain pending, so parent
`task-1.4` stays "planned" until all four land.

## Features
- `ContentStore.Read(fileID uint64) ([]byte, error)`: resolves the same deterministic
  `content/<fileID>.v1.md` path used by create/write, and returns the file's bytes
  unchanged (byte-faithful round trip, no transformation or normalization).
- Distinct not-found signaling: reading a `fileID` with no corresponding content file
  returns a recognizable "not found" error distinct from other I/O failures, so callers
  can branch on absence vs. unexpected error.

## Impact
The content store now supports both halves of the core storage contract established in
task-1.4.1: durable write and byte-faithful read of file content. This unblocks the
remaining content-store subtasks (update/versioning, deletion/GC) which build on both
the write and read paths, and gives the rest of Phase 1 Storage core its first
functioning read API for file bodies.

Known non-blocking follow-ups (tracked in `.cdr/index/regression.jsonl` if promoted;
not blocking this subtask, expected to be picked up by later subtasks in this epic or a
small test-hardening pass):
- No test coverage for the "catalog entry exists but content file is missing or
  unreadable" error branch (distinct from the already-tested "no catalog entry" case).
- Empty-content round-trip (write zero bytes, then read) is untested.
- The specific error-path distinguishability for the missing-file case is not pinned
  down via `errors.Is`/sentinel-error assertions in tests.

## Verification
- verdict: PASS_WITH_COMMENTS
- run_id: 2026-07-04-050-verification

## Release Notes
Added a full-file read API to the content store, completing the write/read round trip
for on-disk file content (`content/<fileID>.v1.md`).
