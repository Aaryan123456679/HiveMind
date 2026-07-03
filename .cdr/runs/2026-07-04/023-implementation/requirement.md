# Requirement — task-1.2.3

Source: `gh issue view 2`, checklist item 1.2.3 (verbatim):

> - [ ] **1.2.3 — Insert (node splitting on overflow)**
>   - Acceptance criteria: Inserting keys beyond a node's capacity triggers a
>     correct split (median promoted to parent, keys partitioned correctly);
>     the tree remains balanced and all previously inserted keys remain
>     lookup-able after splits.
>   - Test spec: `go test ./engine/btree/... -run TestInsertSplit`: insert
>     enough keys to force multiple levels of splitting, assert full-tree
>     lookup correctness afterward and structural invariants (sorted keys,
>     correct fanout).
>   - Impacted modules: `engine/btree/insert.go, engine/btree/insert_test.go`

Context notes:
- `task-1.2.1` (node layout/serialization) and `task-1.2.2` (point lookup +
  `NodeStore`) are both `verified` in `.cdr/index/task.jsonl`.
- 1.2.2's verification flagged a gap: internal nodes with >= 2 separator keys
  were never exercised because `lookup_test.go`'s `buildTestTree` scaffolding
  only builds single-key internal nodes. This subtask's multi-insert internal
  split naturally produces that shape and must close that gap via the
  `Lookup` integration test.
- `buildTestTree` in `lookup_test.go` is explicitly documented as test-only
  scaffolding, NOT to be reused as or mistaken for the real insert path.
