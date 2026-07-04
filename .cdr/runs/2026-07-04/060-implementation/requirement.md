# Requirement (verbatim, subtask 1.5.1 of issue #5)

Issue #5: "Single-threaded end-to-end integration & smoke test" (Phase 1 epic).
Issues #1 (catalog), #2 (btree), #3 (WAL), #4 (content I/O, incl.
`engine/catalog/content.go`'s Create/Read/Append and `RecoverFromWAL`) are all
implemented and verified.

## Subtask 1.5.1 — Cross-package integration test: catalog + btree + wal + content, single-threaded happy path

- **Acceptance criteria**: A single test wires catalog, btree, wal, and content
  together to create several topic files, append to them, look them up by
  path, and read them back, all single-threaded, with correct results
  end-to-end.
- **Test spec**: `go test ./engine/... -run TestStorageCoreIntegration -race`
  (single-threaded workload): create N files via btree-insert+catalog+content,
  append, prefix-scan, full-read, assert consistency across all four modules.
- **Impacted modules**: `engine/integration_test.go` (new file, at the
  `engine/` module root, NOT inside a subpackage — check `go.work`/module
  layout to confirm where this file belongs and what package name it should
  use, likely `package engine_test` or similar; check if `engine/` root
  already has any Go files/package declared).

No new production code expected; this is integration-test-only scope.
