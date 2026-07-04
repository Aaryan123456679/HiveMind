# task-1.5.1 — Cross-package integration test: catalog + btree + wal + content, single-threaded happy path

## Summary

First of two subtasks under `task-1.5` (issue #5, cross-package integration
coverage for Phase 1 storage core). Adds `TestStorageCoreIntegration`, a
genuine single-threaded integration test that wires the real `catalog`,
`btree`, `wal`, and `content` (via `catalog.ContentStore`) packages together
the way an actual caller would — no mocks or fakes of any of the four
packages — and proves that a fileID's path, metadata, and content resolution
stay consistent across all of them.

## Features

- `engine/integration_test.go`: `TestStorageCoreIntegration`, exercising the
  full write path (allocate fileID -> WAL-log the B+Tree path insert via
  `wal.NewBTreeInsertRecord` + `wal.AppendAndApply` -> apply to the in-memory
  B+Tree -> `ContentStore.Create`/`Append`) across 8 topic files under two
  distinct path prefixes.
- Per-file cross-module assertion chain: for every `PrefixScan` result, the
  resolved `FileID` is checked against the expected path owner, then
  `Catalog.Get(FileID).SizeBytes` is checked against actual written content
  length, then `ContentStore.Read(FileID)` is byte-compared against the
  expected content — repeated independently via point `btree.Lookup` as a
  second, non-aggregate proof path.
- Reuses 1.4.3's real `ContentStore.Append` threshold-crossing return value
  directly (no reimplementation of threshold arithmetic), confirming the
  signal survives being wired through a real multi-package call chain.
- Negative-path coverage using real production code, not stubs: nonexistent
  prefix scan, nonexistent path lookup, and unallocated fileID lookup on
  `Catalog.Get`.

## Impact

Phase 1's four storage-core packages (`btree`, `catalog`, `wal`, `content`)
now have one passing test that proves they compose correctly end-to-end for
the single-threaded happy path, closing the gap where each package was only
verified in isolation. No production source was touched — this is pure test
coverage. `task-1.5` remains open pending subtask 1.5.2 (issue #5, part 2 of
2), which is expected to add WAL-replay / crash-recovery-adjacent
cross-package coverage.

Known non-blocking follow-ups (flagged in verification, not blocking): no
explicit single-entry-prefix-scan case is asserted independently (implicitly
covered since each prefix here has 4 entries, not 1); WAL replay /
crash-recovery is intentionally out of scope for this subtask (deferred to
1.5.2); the test is currently one monolithic function rather than split into
reusable helpers.

## Verification

- Verdict: PASS_WITH_COMMENTS
- Run: 2026-07-04-061-verification

## Release Notes

Added a cross-package integration test proving that the B+Tree index,
catalog metadata, write-ahead log, and content store all agree on a given
file's path, size, and byte content when used together the way a real
caller would. No user-facing behavior change.
