# Subtask 2a.2.3 — GC correctness test under concurrent reader/writer/compactor load

(verbatim from GitHub issue #7, last subtask under task-2a.2 Epoch-based GC)

Acceptance criteria: Running writers, readers, and the compactor concurrently never
reclaims a version still referenced by an in-flight snapshot.

Test spec: `go test ./engine/mvcc/... -race -run TestGCUnderConcurrency`: long-running
readers holding old snapshots concurrently with writers advancing versions and the
compactor running, assert no active snapshot's version is ever deleted.

Impacted modules: `engine/mvcc/gc_test.go`.

Predecessor subtasks (done, verified):
- 2a.2.1: `EpochManager` refcounting (gc.go)
- 2a.2.2: `RunCompaction` compactor + fixed TOCTOU race in `NewSnapshot`'s
  acquire-order (read.go)
