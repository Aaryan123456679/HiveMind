# Requirement (verbatim, gh issue view 9, subtask 2a.4.1)

Issue #9: [2a] Latch-crabbing B+Tree concurrency (engine/btree/)
Epic: Phase 2a: Concurrency primitives (MVCC + striped locking + latch-crabbing B+Tree + epoch GC)

## 2a.4.1 — Per-node latch (mutex) + version counter fields added to node layout

- Acceptance criteria: Every node carries a latch and a version counter that
  increments on any structural mutation to that node.
- Test spec: `go test ./engine/btree/... -run TestNodeLatchFields -race`: mutate a
  node, assert its version counter increments exactly once per mutation.
- Impacted modules: `engine/btree/node.go`, `engine/btree/node_test.go`.

This is the first of task-2a.4's 5 subtasks (2a.4.1-2a.4.5), the last task of Phase
2a. Downstream subtasks (2a.4.2 crabbing insert, 2a.4.3 crabbing delete, 2a.4.4
optimistic read, 2a.4.5 mixed-workload race test) all build directly on the
locking/versioning protocol chosen here.
