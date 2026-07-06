# Plan

1. Root-cause via temporary, non-committed minimal repro (done; see
   architecture-discovery.md).
2. Structural fix (root cause 1): in `crabInsertOnce` (insert.go),
   `crabDeleteOnce` (delete.go), and `Tree.Lookup`'s optimistic read
   (lookup.go), change the leaf-level "move right" stay-condition from
   `len(nextLeaf.Keys) > 0 && path < nextLeaf.Keys[0]` to
   `len(nextLeaf.Keys) == 0 || path < nextLeaf.Keys[0]`, so an empty sibling
   (a Delete-tombstoned leaf awaiting repair) is never treated as a move-right
   target, symmetric across all three call sites.
2b. Structural fix (root cause 2, found via full-scale validation):
   `repairEmptyLeafAtParent`'s borrow-from-left and merge-into-left branches
   (delete.go) now check `left.NextLeaf == leafID` before splicing, retrying
   (release all latches, return `true, nil`) if a concurrent not-yet-
   propagated split of `left` is detected, instead of orphaning the split's
   new right-half node.
2c. Structural fix (root cause 3, found via full-scale validation): reverse
   the write order in `insertIntoLeafAndPropagate`'s leaf-split path and
   `propagate`'s internal-node-split path -- write the brand-new right-half
   node BEFORE the existing left node whose write publishes the pointer to
   it -- so Tree.Lookup's lock-free optimistic reads can never observe a
   reference to a not-yet-written node.
2d. Test-helper fix (found via full-scale validation, not a production bug):
   relax `assertStructuralInvariants`' (insert_test.go) LowKey check from
   exact-equality-to-actual-subtree-min to never-exceeds-actual-subtree-min,
   matching `InternalNode.LowKey`'s own documented fixed-at-creation
   contract (Delete legitimately raises a subtree's true min over time
   without needing to revise any ancestor's LowKey).
3. Add a committed regression test to `btree_test.go` (small-scale,
   deterministic-ish, fast) reproducing the minimal 20-goroutine
   insert-only/delete-only interleaving, run at high `-count` for confidence.
4. Validate: minimal repro at `-count=100`+; full `TestConcurrentMixedWorkload`
   under `-race` with explicit generous `-timeout`; full `engine/btree` suite
   (`go test ./btree/... -race -timeout ...`), zero regressions; whole-module
   `go test ./... -timeout ...`.
5. self-consistency.json, one local commit, handoff.json, update
   `.cdr/index/task.jsonl`.
