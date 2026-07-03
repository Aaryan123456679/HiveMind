# Fix plan: 1.2.4 — repairEmptyLeaf isLeaf type-confusion data-loss bug

## Requirement
`.cdr/runs/2026-07-04/028-verification/verification.json` returned
CHANGES_REQUESTED for one critical, reproducible finding: `repairEmptyLeaf`
(engine/btree/delete.go) discards the `isLeaf` bool returned by
`store.ReadNode` at all four sibling-inspection call sites (left/right
borrow, left/right merge) and unconditionally decodes the result as a
`LeafNode`. A same-parent sibling of an emptied leaf can legitimately be an
INTERNAL node — a shape produced by Delete's own grandparent-splice repair
(`shrinkParentAfterMerge`), which can promote a bare surviving leaf up to sit
directly under a grandparent, alongside sibling children that remain
internal. When the merge fallback hits such a sibling, it decodes it as a
zero-valued `LeafNode`, "merges" nothing, and splices the sibling's real
pointer/key out of the parent — permanently detaching that sibling's entire
live subtree. Verification reproduced this deterministically at 40,000
sequential inserts + sequential delete of keys 0..39899 (fails at i=15525,
dropping an internal node with 161 keys / ~170 children).

## Architecture discovery
- `engine/btree/lookup.go`'s `NodeStore.ReadNode` contract: `(isLeaf bool,
  leaf LeafNode, internal InternalNode, err error)` — exactly one of
  leaf/internal is populated, indicated by `isLeaf`.
- `engine/btree/insert_test.go`'s `assertStructuralInvariants` does **not**
  check uniform leaf depth — only internal-node fanout/sorted-keys and a
  full, ordered, exactly-once leaf-chain traversal via `NextLeaf`. So the
  leaf-adjacent-to-internal-sibling shape produced by the grandparent splice
  is not itself a violated invariant under this codebase's own definition of
  "structurally consistent" — it's an atypical but not inherently invalid
  shape.
- `shrinkParentAfterMerge`'s grandparent-splice bound reasoning (never
  changes the grandparent's own key/child *count*, so no further
  propagation is structurally necessary) is logically sound in isolation and
  was already confirmed correct by verification — it is not itself the bug.

## Judgment call: which fix strategy
Two options were offered: (a) change the grandparent splice itself so the
mixed-type-sibling shape never arises, or (b) make `repairEmptyLeaf`
correctly handle a same-parent sibling of the wrong type without ever
merging/splicing through the mismatch.

**Chosen: (b).** Rationale:
- The grandparent splice's own bounding argument was already confirmed
  correct by verification, and this codebase's own structural-invariant
  checker (`assertStructuralInvariants`) does not require uniform leaf
  depth — so the shape the splice can produce is not itself an established
  invariant violation in this codebase; treating it as illegitimate and
  redesigning the splice would be a broader, riskier change than the
  problem strictly requires, and verification's own root-cause writeup
  explicitly frames it as "a legitimate consequence of Delete's own
  grandparent-splice repair", not as a bug in the splice itself.
- The actual, narrow, unambiguous bug is exactly what verification
  pinpointed: `repairEmptyLeaf` blindly treating an arbitrary sibling as a
  `LeafNode` regardless of its real type. Fixing that locally is a minimal,
  well-contained change that directly closes the data-loss hole without
  touching the (already-verified-correct) splice/root-collapse/empty-tree
  logic, per the instruction to keep this a narrow fix rather than a
  redesign.

## Implementation
In `repairEmptyLeaf`:
1. Added a defensive `len(parent.Children) < 2` guard up front (preserves
   the original defensive invariant check that used to live inline in the
   merge-fallback branch).
2. Read each existing same-parent sibling (left/right) exactly once via
   `store.ReadNode`, and only ever mark it "usable" (`haveLeft`/`haveRight`)
   if `isLeaf == true`. An internal sibling is simply not a candidate.
3. Borrow-from-left / borrow-from-right checks now gate on
   `haveLeft`/`haveRight` in addition to the existing "has more than one key
   to spare" check.
4. Merge fallback now only merges into a sibling that is `haveLeft` /
   `haveRight` (i.e. an actual leaf); it never decodes/splices through an
   internal sibling.
5. If neither sibling is usable (both are internal — the pathological
   post-splice shape), `repairEmptyLeaf` returns the tree unchanged: the
   leaf stays as the empty (0-key) `LeafNode` already written by `Delete`'s
   caller before `repairEmptyLeaf` was invoked. It contributes zero keys to
   the leaf-chain traversal and remains reachable/correctly `NextLeaf`-linked
   — an accepted, tombstoned underflow, consistent with this subtask's
   existing tombstone-until-empty policy, rather than ever merging/splicing
   through a type mismatch.

No other logic (root collapse, empty-tree-after-delete convention, absent-key
handling, leaf borrow/merge among same-type siblings, `shrinkParentAfterMerge`
itself) was touched.

## Validation matrix
| Requirement | Test |
|---|---|
| Silent data loss via type-mismatched sibling is eliminated | `TestDeleteThreeLevelNoSiblingTypeMismatchDataLoss` (new) — real 40,000-key Insert-built tree (confirmed >=3 levels), sequential delete of a 39,900-key prefix (mirroring verification's own 0..39899 repro), asserts every remaining (non-deleted) key is still found via `Lookup` with its original fileID (`assertAllLookupable`), plus `assertStructuralInvariants`/`assertNoOrphanedPointers` |
| No regression to leaf borrow/merge among same-type siblings | `TestDeleteLeafMerge`, `TestDeleteInternalMerge` (existing, unmodified) |
| No regression to root collapse / empty-tree / absent-key handling | `TestDeleteEmptyTree`, `TestDeleteAbsentKey`, `TestDeleteSingleLeaf`, `TestDeleteEmptiesSingleLeafTree` (existing, unmodified) |
| Full package regression | `go test ./engine/btree/... -race -v -count=1` |

## Self-consistency (internal only — not verification)
- `go build ./engine/...` — pass
- `go vet ./engine/...` — pass
- `go test ./engine/btree/... -race -v -count=1` — pass, all prior + new
  tests, no regressions (~21s wall time; new test itself ~9s due to real
  40,000-key Insert + ~39,900-key Delete)
- Sanity check that the new test is a real regression test (not vacuously
  passing): `git stash push -- engine/btree/delete.go` (keeping the new
  test) then re-ran the new test alone against the *pre-fix* delete.go —
  confirmed it fails (`Delete("topic15526/page"): expected found=true, got
  false`), then `git stash pop` to restore the fix. Confirms the test
  actually exercises and would have caught the original bug.
