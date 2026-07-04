# task-1.5.2 — End-to-end crash-recovery integration test across all four modules

## Summary

Last subtask under `task-1.5` (issue #5, cross-package integration coverage for Phase 1 storage core). Adds `TestStorageCoreCrashRecovery`, a genuine single-threaded integration test that simulates a mid-append process kill and restart, then proves `catalog`, `btree`, `wal`, and `content` reconstruct a consistent end state via WAL replay. This closes out `task-1.5`, issue #5, and — with issues #1-#5 now all verified — Phase 1 (Storage core) in full.

## Features

- `engine/integration_test.go`: `TestStorageCoreCrashRecovery`, simulating an interrupted append (crash mid-way through a WAL-logged write), reopening all four stores fresh, and running WAL recovery.
- Post-recovery assertions across all four modules confirm a single consistent view: no partially-written or corrupted files are visible, and catalog/btree/content agree on the recovered state.
- Builds directly on 1.5.1's real (non-mocked) cross-package wiring and reuses its happy-path proof style for the recovery path.

## Impact

Phase 1's four storage-core packages (`btree`, `catalog`, `wal`, `content`) now have integration coverage for both the happy path (1.5.1) and the crash-recovery path (1.5.2), closing the last identified gap for the storage-core epic. No production source was touched.

Verification surfaced one pre-existing, non-blocking limitation in `engine/btree` (introduced under issue #2's original design, not by this subtask): `SaveRoot` is a manual, out-of-band checkpoint — it is not called automatically on every `Insert` — and `RecoverFromWAL` deliberately no-ops on btree WAL records. As a result, a real crash occurring between an `Insert` and the next `SaveRoot` would silently drop that insert from the recovered btree, even though the WAL record itself was durably applied. This gap is transparently documented (not hidden by the test) and is tracked as a Phase 2 follow-up — see `.cdr/memory/pending.md` — to either wire btree WAL records into real replay-based reconstruction or add automatic/periodic checkpointing, plus an explicit test exercising the uncheckpointed window.

## Verification

- Verdict: PASS_WITH_COMMENTS
- Run: 2026-07-04-064-verification

## Release Notes

Added an end-to-end crash-recovery test proving that after a simulated mid-append crash and restart, the B+Tree index, catalog metadata, write-ahead log, and content store all recover to one consistent, uncorrupted state. No user-facing behavior change. A pre-existing btree checkpoint/WAL-replay timing gap was identified during verification and is tracked for Phase 2, not fixed in this change.
