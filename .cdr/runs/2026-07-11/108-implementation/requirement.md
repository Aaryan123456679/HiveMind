# Requirement — issue #50, subtask 4.5.12.3

Fetched via `gh issue view 50` (full text confirmed).

Subtask 4.5.12.3 — "Add descent test for Lookup's internal-node routing on a
>=2-key node (low, coverage-completeness)":

- Acceptance criteria: `Tree.Lookup`'s internal-node branch (`0 < i <
  len(Keys)`) must be exercised by a dedicated test using a >=2-key internal
  node, not just incidentally by larger integration tests.
- Test spec: `go test ./engine/btree/... -run TestLookupInternalNodeMultiKeyRouting`
- Impacted modules: `engine/btree/lookup_test.go`

Sibling subtasks in the same issue (NOT in scope here):
- 4.5.12.1 — already resolved (insert_test.go)
- 4.5.12.2 — already verified PASS (node.go/node_test.go)
- 4.5.12.4 — insert.go/insert_test.go (NodeAllocator cross-check), deferred
- 4.5.12.5 — scan.go doc comment, deferred
- 4.5.12.6 — btree_test.go persistence regression, deferred
- 4.5.12.7 — docs/LLD/btree.md sync pass, deferred

No overlap with this subtask's target file (`engine/btree/lookup_test.go`).
