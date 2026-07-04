# task-1.4.4 — Durability round-trip test across simulated restart

## Summary

Closes out the final subtask (4 of 4) of GitHub issue #4 (Phase 1 Storage core /
single-version .md content read/write). Adds `RecoverFromWAL`, the missing piece
that lets `Catalog` rebuild its in-memory index after a process restart by
replaying `CatalogPut`/`CatalogDelete` records through the already-verified,
checkpoint-aware `wal.Replay`, and proves the full round-trip (write, append,
simulated restart via fresh `FileManager`/`Catalog`/`wal.Writer`/`ContentStore`,
then read) returns byte-identical content. With this subtask verified, all four
subtasks of `task-1.4` (1.4.1-1.4.4) are now complete.

## Features

- `engine/catalog/recovery.go`: `RecoverFromWAL(...)`, a thin catalog-specific
  consumer of `wal.Replay` that re-applies `CatalogPut`/`CatalogDelete` WAL
  records into a freshly-opened `Catalog`'s in-memory index, closing the gap
  where a reopened catalog previously started with an empty index regardless
  of prior durable writes.
- `TestContentDurabilityRestart`: a genuine two-generation restart simulation
  — first generation writes and appends content and is fully closed (no
  in-process state carried over), second generation reopens against the same
  on-disk root, runs recovery, and reads back content that matches the
  pre-restart bytes exactly.
- Recovery correctly inherits `wal.Replay`'s checkpoint-forward semantics
  (no full-from-genesis replay), so restart cost scales with the
  un-checkpointed WAL tail rather than total WAL history.

## Impact

The content store's write, read, and append paths (1.4.1-1.4.3) now durably
survive a process restart with a dedicated, passing round-trip test, closing
out GitHub issue #4 in full. `task-1.4` (a pure container task with no
deliverable of its own beyond its four subtasks) is also marked verified as a
result — all Phase 1 single-version content I/O work is complete and
verified end-to-end.

Known non-blocking follow-ups (flagged in verification, not blocking):
only one restart scenario is exercised today (create + appends, then
restart); not yet covered are a zero-prior-writes restart, a restart
following a WAL torn tail, and replay of an actual `CatalogDelete` record
(dead code today since `ContentStore` never calls `Catalog.Delete`). None of
these are correctness defects in the shipped code — recommended as a
fast-follow once a real caller starts exercising delete or torn-tail-adjacent
restart paths.

## Verification

- Verdict: PASS_WITH_COMMENTS
- Run: 2026-07-04-058-verification

## Release Notes

The content store now survives a process restart: content written and
appended before a restart is fully recoverable and reads back byte-for-byte
identical afterward. This closes out issue #4 (single-version .md content
read/write) in full.
