# Requirement — Subtask 1.2.4

Source: `gh issue view 2` (Epic "Phase 1: Storage core (single-threaded)"), checklist item 1.2.4,
pulled verbatim.

## Verbatim from issue #2

- [ ] **1.2.4 — Delete (simplified rebalancing: merge-or-tombstone strategy, documented choice)**
  - Acceptance criteria: Deleting a key removes it from lookups; tree remains internally
    consistent (no orphaned child pointers) across interleaved insert/delete sequences.
  - Test spec: `go test ./engine/btree/... -run TestDelete`: insert/delete sequence, then a
    structural-invariant check.
  - Impacted modules: `engine/btree/delete.go, engine/btree/delete_test.go`

The issue's own text is itself compressed/mangled by GitHub's markdown -> plain text rendering in
several places (words dropped, e.g. "Impacted modules" and "structural-invariant" run together in
places), but the one load-bearing, must-match-verbatim token is unambiguous and repeated
identically in both the checklist title's Test-spec line and the sibling subtasks' analogous
lines: the required run command is

    go test ./engine/btree/... -run TestDelete

This subtask's test file MUST define a top-level `func TestDelete(t *testing.T)` (mirroring how
1.2.3 defined `TestInsertSplit` as the literal acceptance-test entry point, dispatching to
subtests) so `-run TestDelete` actually exercises real delete-path coverage instead of matching
zero tests. This is exactly the class of issue that caused 1.2.3 CHANGES_REQUESTED in the prior
subtask 1.2.3 iteration per the task instructions -- avoided here by using the literal name.

## Derived acceptance criteria (this run's interpretation)

1. Deleting an existing key: subsequent `Lookup` for that key returns `found=false`.
2. Deleting a key removes it from lookups; tree remains internally consistent (no orphaned child
   pointers) — verified via a structural-invariant checker analogous to insert_test.go's
   `assertStructuralInvariants`, extended to also assert no dangling/duplicate child IDs.
3. Deleting from an empty tree (`rootNodeID == reservedNodeID`) returns `found=false`, no panic,
   no error.
4. Deleting an absent key from a non-empty tree returns `found=false`, no error, tree unchanged.
5. Deleting down to a single leaf requires no rebalancing beyond the leaf itself.
6. At least one delete sequence triggers a real leaf merge or redistribute (borrow) via the actual
   `Insert`-built tree from 1.2.3, not synthetic test scaffolding.
7. At least one delete sequence triggers a real internal-node merge or redistribute at least one
   level above the leaf, again via a real `Insert`-built tree big enough to have multiple internal
   levels.
8. Full integration: after a mixed sequence of inserts and deletes, every remaining key is found
   via `Lookup` with the correct fileID, and every deleted key returns not-found.

## Known LLD/prior-subtask facts consulted

- `docs/LLD/btree.md` defines Delete as one of the four core operations but does not specify a
  numeric underflow threshold, merge vs. redistribute preference, or root-collapse convention —
  those are left to this subtask to choose and document (per LLD's "Status: scaffold only").
- 1.2.1 defined `NodeSize` (4096), leaf/internal encode/decode, `noSibling` (0) as the NextLeaf
  sentinel.
- 1.2.2 defined `NodeStore`, `reservedNodeID` (0), `descendToLeaf` (in `lookup.go`) as the single
  shared descent helper Insert and Lookup both already use.
- 1.2.3 defined `NodeAllocator` (monotonic, no free-list, documented known gap), `Insert` with
  leaf/internal split, median promotion, and the empty-tree bootstrap convention
  (`rootNodeID == reservedNodeID` means "no root yet").
