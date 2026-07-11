# Plan — subtask 4.5.1.5

1. Confirm via `git log` that both the "orphan fix" and "publish-last fix"
   already shipped in commit `b31328f`, and read both functions in full in
   current source to confirm unchanged.
2. Add two nil-in-production, test-only hooks to `engine/btree/insert.go`:
   `splitPublishHook(newChildID uint64)` (fires right after a split's new
   node is written+unlocked, before the publish write) and
   `prePropagateHook(oldChildID uint64)` (fires right after the publish
   write completes and split-local latches are released, before `propagate`
   is entered). Install both at the leaf-split site
   (`insertIntoLeafAndPropagate`) and the internal-split site
   (`Tree.propagate`), mirroring `crabRetryHook`/`optimisticReadHook`/
   `unlockOrderHook`'s existing shape exactly.
3. Write `TestRepairEmptyLeafOrphanRegression` (delete_test.go): construct a
   minimal parent+2-leaf tree via fixed allocator IDs, size the left leaf to
   one key below overflow, pre-construct the right leaf as already-empty.
   Use `prePropagateHook` to pause a real `Tree.Insert`-triggered split of
   the left leaf right after it completes but before `propagate` links the
   new right-half node into the parent. While paused, call the real
   `repairEmptyLeafAtParent` directly and assert it returns `retry=true` (not
   an orphan), and that every latch it touched is fully released afterward.
   Release the pause, let the insert finish, finish the leaf's real repair,
   and assert full structural/lookup/no-orphan correctness.
4. Write `TestSplitPublishLastOrderingRegression` (insert_test.go): construct
   a single, near-full leaf, use `splitPublishHook` to pause a real
   `Tree.Insert`-triggered split at the exact publish boundary, and directly
   assert `store.ReadNode` on the new node succeeds at that exact point
   (proving the new node is durably written before it is ever published).
   Release, let insert finish, assert full structural/lookup correctness.
5. While writing test 3, discovered a genuine, previously-uncaught
   double-Unlock/leaked-latch bug in `repairEmptyLeafAtParent`'s orphan-guard
   retry branch (delete.go). Fixed it minimally (three distinct
   Unlock calls instead of two identical ones), with a doc comment
   explaining the bug and its discovery.
6. Mutation-test both new tests against the pre-fix code (temporarily
   reverting each fix in a scratch copy, confirming each test fails/panics
   exactly as expected, then restoring the correct code byte-for-byte via
   `diff` against a backup) to confirm they are load-bearing, not just
   passing by construction.
7. Run `go build`, `go vet`, and the full `engine/btree` suite under `-race`
   to confirm no regressions.
8. Self-consistency, ONE local commit (delete.go, insert.go, delete_test.go,
   insert_test.go, plus this run's own `.cdr/runs/...` directory only), no
   push, handoff.
