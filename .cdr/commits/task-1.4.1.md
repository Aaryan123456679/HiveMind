# task-1.4.1 — Content store layout: content/<fileID>.v1.md create/write path, WAL-before-apply

## Summary
Closes out subtask 1 of 4 (GitHub issue #4, Phase 1 Storage core / content store) by
implementing the on-disk content store layout and the create/write path for catalog
content bodies: `content/<fileID>.v1.md`, written durably behind the existing
WAL-before-apply contract. This is the first of four subtasks under the parent
`task-1.4` (content store) epic; subtasks 1.4.2-1.4.4 remain pending, so the parent
task stays "planned" until all four land.

## Features
- Deterministic content path derivation: given a catalog `fileID`, resolves to
  `content/<fileID>.v1.md` under the store root, with no possibility of path
  traversal since `fileID` is a `uint64` (never caller-supplied string content).
- Byte-faithful content create/write: content is persisted exactly as given, with no
  transformation, encoding conversion, or newline normalization.
- WAL-before-apply durability: content creation is journaled through the existing
  WAL `AppendAndApply` path (fsync-before-return) before the on-disk file mutation is
  considered applied, consistent with the crash-safety contract established for
  WAL in GitHub issue #3 / task-1.3.

## Impact
The catalog now has a working, durable content-body storage layer for the first
time: callers can create and persist file content under a well-defined,
collision-free on-disk layout, with the same WAL-before-apply crash-safety
guarantees already proven for the rest of Phase 1 Storage core. This unblocks the
remaining content-store subtasks (update/versioning, read path, deletion/GC) under
task-1.4.

Known non-blocking follow-ups (tracked in `.cdr/index/regression.jsonl`, to be
picked up by later subtasks in this epic, not blocking this one):
- Create has no duplicate/already-exists guard; current semantics are a silent
  upsert, and this behavior is untested.
- No test coverage yet for empty or very-large content bodies.
- `docs/LLD/catalog.md` and `docs/LLD/wal.md` remain stale/undocumented with
  respect to the new `ContentStore`.

## Verification
- verdict: PASS_WITH_COMMENTS
- run_id: 2026-07-04-047-verification

## Release Notes
Added the on-disk content store layout (`content/<fileID>.v1.md`) and its
create/write path, backed by write-ahead-log durability, as the first building
block of the catalog's content storage subsystem.
