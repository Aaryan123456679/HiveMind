# Subtask 2a.3.1 — Concurrent stress test across many fileIDs verifying stripe isolation

(GitHub issue #8, "Catalog concurrent-correctness hardening", engine/catalog/)
First subtask under task-2a.3, third task of Epic Phase 2a. Task-2a.1 (MVCC) and
task-2a.2 (epoch-GC) are fully done and verified.

**Acceptance criteria**: Many goroutines performing Put/Get/Delete across many
distinct fileIDs complete with correct final state and no lock contention across
unrelated stripes causing incorrect results.

**Test spec**: `go test ./engine/catalog/... -race -run TestStripedConcurrencyStress`:
high-goroutine-count CRUD workload across a wide fileID range, assert final catalog
state matches a serial-execution oracle.

**Impacted modules**: `engine/catalog/catalog_test.go` (test-only; no production code
changes).
