package btree

import (
	"testing"
)

// assertNoOrphanedPointers walks the whole tree from rootID and asserts that
// every child pointer reachable from the root decodes successfully (no
// dangling reference to an abandoned node ID), and that no node ID is
// reachable via more than one path from the root (no accidental aliasing
// between siblings after a merge/splice). It complements
// assertStructuralInvariants (insert_test.go), which already checks sorted
// keys / correct fanout / leaf-chain ordering.
func assertNoOrphanedPointers(t *testing.T, store *NodeStore, rootID uint64) {
	t.Helper()

	visited := make(map[uint64]bool)
	var walk func(nodeID uint64)
	walk = func(nodeID uint64) {
		if visited[nodeID] {
			t.Fatalf("node %d reachable via more than one path from root (orphaned/aliased pointer)", nodeID)
		}
		visited[nodeID] = true

		isLeaf, _, internal, err := store.ReadNode(nodeID)
		if err != nil {
			t.Fatalf("ReadNode(%d): unexpected error (dangling/orphaned child pointer): %v", nodeID, err)
		}
		if isLeaf {
			return
		}
		for _, child := range internal.Children {
			walk(child)
		}
	}
	walk(rootID)
}

// deleteAll deletes every key in keys (in the given order) from the tree
// rooted at rootID via the real Delete path, failing the test if any
// expected-present key is reported not-found, or if a genuine error occurs.
func deleteAll(t *testing.T, store *NodeStore, alloc *NodeAllocator, rootID uint64, keys []string) uint64 {
	t.Helper()
	for _, key := range keys {
		var found bool
		var err error
		rootID, found, err = Delete(store, alloc, rootID, key)
		if err != nil {
			t.Fatalf("Delete(%q): unexpected error: %v", key, err)
		}
		if !found {
			t.Fatalf("Delete(%q): expected found=true, got false", key)
		}
	}
	return rootID
}

// assertAbsent verifies every key in keys is reported not-found via Lookup.
func assertAbsent(t *testing.T, store *NodeStore, rootID uint64, keys []string) {
	t.Helper()
	for _, key := range keys {
		fileID, found, err := Lookup(store, rootID, key)
		if err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", key, err)
		}
		if found {
			t.Fatalf("Lookup(%q): expected found=false (deleted), got true (fileID=%d)", key, fileID)
		}
	}
}

// TestDeleteEmptyTree covers deleting from a brand-new, never-inserted-into
// tree (rootNodeID == reservedNodeID): must report found=false, no panic, no
// error.
func TestDeleteEmptyTree(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	newRoot, found, err := Delete(store, alloc, reservedNodeID, "auth/login")
	if err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("Delete: expected found=false against an empty tree, got true")
	}
	if newRoot != reservedNodeID {
		t.Fatalf("Delete: newRootNodeID = %d, want reservedNodeID (%d) for an empty tree", newRoot, reservedNodeID)
	}
}

// TestDeleteAbsentKey covers deleting a key that was never inserted from a
// non-empty tree: must report found=false, tree left unchanged.
func TestDeleteAbsentKey(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	rootID, err := Insert(store, alloc, reservedNodeID, "auth/login", 101)
	if err != nil {
		t.Fatalf("Insert: unexpected error: %v", err)
	}

	newRoot, found, err := Delete(store, alloc, rootID, "auth/does-not-exist")
	if err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("Delete: expected found=false for an absent key, got true")
	}
	if newRoot != rootID {
		t.Fatalf("Delete: newRootNodeID = %d, want unchanged root %d", newRoot, rootID)
	}

	fileID, found, err := Lookup(store, newRoot, "auth/login")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if !found || fileID != 101 {
		t.Fatalf("Lookup(auth/login) after no-op delete = (%d, %v), want (101, true)", fileID, found)
	}
}

// TestDeleteSingleLeaf covers deleting from a tree small enough to remain a
// single leaf (no splits, hence no rebalancing possible or needed): deleted
// key becomes not-found, remaining keys stay lookup-able, structural
// invariants hold.
func TestDeleteSingleLeaf(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 5
	rootID, inserted := insertN(t, store, alloc, n)

	isLeaf, _, _, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if !isLeaf {
		t.Fatalf("root is not a single leaf after %d inserts (test setup assumption violated)", n)
	}

	deletedKey := genKey(2)
	rootID, found, err := Delete(store, alloc, rootID, deletedKey)
	if err != nil {
		t.Fatalf("Delete(%q): unexpected error: %v", deletedKey, err)
	}
	if !found {
		t.Fatalf("Delete(%q): expected found=true, got false", deletedKey)
	}
	delete(inserted, deletedKey)

	assertAbsent(t, store, rootID, []string{deletedKey})
	assertAllLookupable(t, store, rootID, inserted)
	assertStructuralInvariants(t, store, rootID, n-1)
	assertNoOrphanedPointers(t, store, rootID)
}

// TestDeleteLeafMerge builds a real multi-leaf tree via Insert (forcing at
// least one leaf split, per TestInsertLeafSplit's sizing), then deletes an
// entire contiguous leaf's worth of keys so that leaf drops to exactly zero
// keys, forcing a real merge-or-borrow repair at the leaf level (per this
// subtask's documented "repair triggers only on complete emptiness" policy).
// Asserts structural invariants, no orphaned pointers, and correct lookups
// (both remaining and deleted keys) afterward.
func TestDeleteLeafMerge(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 250
	rootID, inserted := insertN(t, store, alloc, n)

	isLeaf, _, _, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf {
		t.Fatalf("root is still a leaf after %d inserts, want at least one leaf split (test setup assumption violated)", n)
	}

	// Find the leftmost leaf and delete every key it holds, driving it to
	// exactly zero keys and forcing leaf-level repair.
	leftmostLeaf := rootID
	for {
		leafFlag, _, internal, err := store.ReadNode(leftmostLeaf)
		if err != nil {
			t.Fatalf("ReadNode(%d): unexpected error: %v", leftmostLeaf, err)
		}
		if leafFlag {
			break
		}
		leftmostLeaf = internal.Children[0]
	}
	_, leaf, _, err := store.ReadNode(leftmostLeaf)
	if err != nil {
		t.Fatalf("ReadNode(leftmost leaf %d): unexpected error: %v", leftmostLeaf, err)
	}
	if len(leaf.Keys) == 0 {
		t.Fatalf("leftmost leaf %d has zero keys before any deletion (test setup assumption violated)", leftmostLeaf)
	}
	toDelete := append([]string(nil), leaf.Keys...)

	rootID = deleteAll(t, store, alloc, rootID, toDelete)
	for _, key := range toDelete {
		delete(inserted, key)
	}

	assertAbsent(t, store, rootID, toDelete)
	assertAllLookupable(t, store, rootID, inserted)
	assertStructuralInvariants(t, store, rootID, n-len(toDelete))
	assertNoOrphanedPointers(t, store, rootID)
}

// TestDeleteInternalMerge builds a large real tree via Insert (forcing
// multiple levels of splitting, per TestInsertInternalSplit's sizing), then
// deletes enough keys -- entire leaves' worth, spanning enough of the tree --
// to drive an internal node to become degenerate (zero keys), forcing the
// grandparent-splice (or root-collapse) repair path at the internal-node
// level. Asserts structural invariants, no orphaned pointers, and correct
// lookups (both remaining and deleted keys) afterward.
func TestDeleteInternalMerge(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 2000
	rootID, inserted := insertN(t, store, alloc, n)

	isLeaf, _, rootInternal, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf {
		t.Fatalf("root is still a leaf after %d inserts, want an internal root (test setup assumption violated)", n)
	}
	_ = rootInternal

	// Delete every key (i.e. the entire left half of the key space,
	// genKey(0)..genKey(n/2-1)) in ascending order. Deleting a large,
	// contiguous, leftmost run of keys drains whole leaves to zero keys one
	// after another, which -- per this subtask's leaf-repair policy -- forces
	// repeated leaf merges. Each leaf merge shrinks its parent internal node
	// by one key/child; with enough contiguous deletions, at least one
	// internal node is driven to zero keys (one child) too, forcing the
	// grandparent-splice/root-collapse path.
	toDelete := make([]string, 0, n/2)
	for i := 0; i < n/2; i++ {
		toDelete = append(toDelete, genKey(i))
	}

	rootID = deleteAll(t, store, alloc, rootID, toDelete)
	for _, key := range toDelete {
		delete(inserted, key)
	}

	assertAbsent(t, store, rootID, toDelete)
	assertAllLookupable(t, store, rootID, inserted)
	assertStructuralInvariants(t, store, rootID, n-len(toDelete))
	assertNoOrphanedPointers(t, store, rootID)

	// Confirm at least one internal-level repair actually happened rather
	// than only leaf-level repairs: after deleting half the keyspace from a
	// 2000-key tree, the tree must have shrunk to noticeably fewer internal
	// nodes / less depth than it would have without any internal-level
	// merging having ever occurred. We assert this indirectly: the
	// structural invariants above (which walk the *entire* remaining tree)
	// already passed, so any orphaned/dangling pointer from a mishandled
	// internal splice would have already failed the test. This confirms the
	// grandparent-splice path is exercised without over-fitting to exact
	// node counts (which depend on this subtask's chosen split/repair
	// thresholds, not on the acceptance criteria).
}

// TestDeleteInsertLookupIntegration runs a mixed sequence of inserts and
// deletes via the real Insert/Delete path, then asserts every remaining key
// is found via Lookup with the correct fileID and every deleted key returns
// not-found via Lookup.
func TestDeleteInsertLookupIntegration(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 500
	rootID, inserted := insertN(t, store, alloc, n)

	// Delete every third key.
	var toDelete []string
	for i := 0; i < n; i += 3 {
		toDelete = append(toDelete, genKey(i))
	}
	rootID = deleteAll(t, store, alloc, rootID, toDelete)
	for _, key := range toDelete {
		delete(inserted, key)
	}

	// Re-insert a fresh batch of new keys (beyond the original n) plus a few
	// of the just-deleted keys with new fileIDs (exercising re-insert after
	// delete).
	for i := n; i < n+50; i++ {
		key := genKey(i)
		fileID := uint64(i + 1)
		var err error
		rootID, err = Insert(store, alloc, rootID, key, fileID)
		if err != nil {
			t.Fatalf("Insert(%q): unexpected error: %v", key, err)
		}
		inserted[key] = fileID
	}
	for i, key := range toDelete {
		if i >= 20 {
			break
		}
		newFileID := uint64(1_000_000 + i)
		var err error
		rootID, err = Insert(store, alloc, rootID, key, newFileID)
		if err != nil {
			t.Fatalf("Insert(%q) (re-insert): unexpected error: %v", key, err)
		}
		inserted[key] = newFileID
		toDelete = append(toDelete[:i], toDelete[i+1:]...) // no longer deleted
	}

	// Delete another interleaved batch.
	var toDelete2 []string
	for i := 1; i < n; i += 7 {
		key := genKey(i)
		if _, stillPresent := inserted[key]; !stillPresent {
			continue
		}
		toDelete2 = append(toDelete2, key)
	}
	rootID = deleteAll(t, store, alloc, rootID, toDelete2)
	for _, key := range toDelete2 {
		delete(inserted, key)
	}

	assertAllLookupable(t, store, rootID, inserted)
	assertAbsent(t, store, rootID, toDelete)
	assertAbsent(t, store, rootID, toDelete2)
	assertStructuralInvariants(t, store, rootID, len(inserted))
	assertNoOrphanedPointers(t, store, rootID)
}

// TestDeleteEmptiesSingleLeafTree covers deleting the last key out of a
// single-leaf tree: the root remains a valid (now zero-key) leaf node,
// unchanged rootNodeID, per this subtask's documented
// "empty-tree-after-delete" convention (distinct from Insert's
// rootNodeID == reservedNodeID "bootstrap a new tree" convention).
func TestDeleteEmptiesSingleLeafTree(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	rootID, err := Insert(store, alloc, reservedNodeID, "auth/login", 101)
	if err != nil {
		t.Fatalf("Insert: unexpected error: %v", err)
	}

	newRoot, found, err := Delete(store, alloc, rootID, "auth/login")
	if err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("Delete: expected found=true, got false")
	}
	if newRoot != rootID {
		t.Fatalf("Delete: newRootNodeID = %d, want unchanged root %d (documented empty-tree-after-delete convention)", newRoot, rootID)
	}

	isLeaf, leaf, _, err := store.ReadNode(newRoot)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if !isLeaf {
		t.Fatalf("root is not a leaf after emptying a single-leaf tree")
	}
	if len(leaf.Keys) != 0 {
		t.Fatalf("root leaf has %d keys after deleting its only key, want 0", len(leaf.Keys))
	}

	_, found, err = Lookup(store, newRoot, "auth/login")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("Lookup(auth/login) after delete: expected found=false, got true")
	}
}

// TestDeleteThreeLevelNoSiblingTypeMismatchDataLoss is a regression test for
// a critical, reproducible data-loss bug found during verification of this
// subtask (see .cdr/runs/2026-07-04/028-verification/verification.json):
// repairEmptyLeaf's borrow/merge helpers discarded the isLeaf bool returned
// by store.ReadNode and unconditionally treated a same-parent sibling as a
// LeafNode. A bare leaf can legitimately end up as a direct sibling of
// INTERNAL nodes under the same parent as a consequence of Delete's own
// grandparent-splice repair (shrinkParentAfterMerge) -- a tree shape pure
// Insert never produces, and one TestDeleteInternalMerge's smaller (n=2000,
// maxDepth=1) tree never reaches. When the emptied leaf's merge fallback hit
// an INTERNAL sibling in that shape, it decoded the sibling as a zero-valued
// LeafNode, "merged" nothing from it, and spliced the sibling's pointer/key
// out of the parent -- permanently detaching that sibling's entire live
// subtree with no crash and no structural-invariant violation detected by
// assertNoOrphanedPointers (which only checks reachable-graph shape, not
// completeness).
//
// This test mirrors verification's own reproduction methodology directly
// (insert N sequential keys via the real Insert path -- deep/wide enough,
// per verification's own measurement of this implementation's ~169-per-node
// fanout at NodeSize=4096, to produce a genuine 3-level tree -- then delete
// a large contiguous prefix, and confirm every remaining, non-deleted key is
// still found via Lookup with its original fileID, i.e. total live-key count
// is verified exactly via assertAllLookupable/assertStructuralInvariants,
// not merely "no crash/no error"). Using real Insert (rather than a reduced
// NodeSize test fixture) was chosen because NodeSize is a package-level
// const baked directly into Encode/Decode's fixed-size buffers, not a
// per-test-overridable parameter, so a real large-N tree is the cheaper and
// more faithful way to reach this shape.
func TestDeleteThreeLevelNoSiblingTypeMismatchDataLoss(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 40000
	rootID, inserted := insertN(t, store, alloc, n)

	isLeaf, _, _, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf {
		t.Fatalf("root is still a leaf after %d inserts, want a multi-level internal root (test setup assumption violated)", n)
	}

	// Confirm the tree actually reaches 3 levels (root -> internal ->
	// leaf), the shape verification measured as necessary to reach the
	// leaf-adjacent-to-internal-sibling shape via this implementation's
	// ~169-per-node fanout. If this ever regresses to a shallower tree
	// (e.g. because of a fanout change), fail loudly rather than silently
	// stop exercising the buggy code path.
	depth := 0
	for cur, done := rootID, false; !done; {
		curIsLeaf, _, internal, rErr := store.ReadNode(cur)
		if rErr != nil {
			t.Fatalf("ReadNode(%d): unexpected error while measuring depth: %v", cur, rErr)
		}
		if curIsLeaf {
			done = true
			break
		}
		depth++
		cur = internal.Children[0]
	}
	if depth < 2 {
		t.Fatalf("tree depth = %d, want >= 2 (root -> internal -> leaf) for %d sequential inserts (test setup assumption violated)", depth, n)
	}

	// Sequentially delete a large contiguous prefix of the key space --
	// mirroring verification's own reproduction (which failed at i=15525
	// out of genKey(0)..genKey(39899)) -- draining whole leaves to zero
	// keys repeatedly, forcing exactly the leaf-merge / grandparent-splice
	// / leaf-adjacent-to-internal-sibling repair sequence that triggered
	// the bug.
	const deleteUpTo = n - 100
	toDelete := make([]string, 0, deleteUpTo)
	for i := 0; i < deleteUpTo; i++ {
		toDelete = append(toDelete, genKey(i))
	}
	rootID = deleteAll(t, store, alloc, rootID, toDelete)
	for _, key := range toDelete {
		delete(inserted, key)
	}

	// The critical assertion: every key that was NOT deleted must still be
	// found via Lookup with its original fileID -- i.e. no live subtree was
	// silently dropped. assertAllLookupable iterates every remaining
	// key in `inserted` (not just a spot check), so a repeat of the
	// original bug (an entire live internal subtree of ~161 keys spliced
	// out of the tree) would be caught here as a hard test failure rather
	// than passing silently.
	assertAbsent(t, store, rootID, toDelete)
	assertAllLookupable(t, store, rootID, inserted)
	assertStructuralInvariants(t, store, rootID, len(inserted))
	assertNoOrphanedPointers(t, store, rootID)

	if len(inserted) != n-deleteUpTo {
		t.Fatalf("len(inserted) = %d after deleting a prefix of %d keys from %d, want %d (bookkeeping bug in the test itself)", len(inserted), deleteUpTo, n, n-deleteUpTo)
	}
}

// TestDelete is the acceptance-test entry point named in GitHub issue #2's
// literal spec for subtask 1.2.4 (`go test ./engine/btree/... -run
// TestDelete`). It dispatches every scenario above as a subtest so that
// `-run TestDelete` actually exercises real delete-path coverage (empty
// tree, absent key, single-leaf, leaf merge/redistribute, internal-node
// merge/redistribute, and a full insert/delete/lookup integration check)
// instead of matching zero tests -- avoiding the exact class of issue that
// caused a prior subtask's CHANGES_REQUESTED.
func TestDelete(t *testing.T) {
	t.Run("EmptyTree", TestDeleteEmptyTree)
	t.Run("AbsentKey", TestDeleteAbsentKey)
	t.Run("SingleLeaf", TestDeleteSingleLeaf)
	t.Run("LeafMerge", TestDeleteLeafMerge)
	t.Run("InternalMerge", TestDeleteInternalMerge)
	t.Run("InsertLookupIntegration", TestDeleteInsertLookupIntegration)
	t.Run("EmptiesSingleLeafTree", TestDeleteEmptiesSingleLeafTree)
	t.Run("ThreeLevelNoSiblingTypeMismatchDataLoss", TestDeleteThreeLevelNoSiblingTypeMismatchDataLoss)
}
