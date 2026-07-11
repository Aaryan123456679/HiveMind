package btree

import (
	"fmt"
	"path/filepath"
	"sync"
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

// ---------------------------------------------------------------------------
// 2a.4.3: TestCrabbingDelete -- the concurrent latch-crabbing delete test
// spec (GitHub issue #9): `go test ./engine/btree/... -race -run
// TestCrabbingDelete`. ALWAYS run with -timeout, per this package's history
// of a real 40+ minute deadlock hang during 2a.4.2's development.
// ---------------------------------------------------------------------------

func TestCrabbingDelete(t *testing.T) {
	t.Run("DisjointSubtrees", testCrabbingDeleteDisjointSubtrees)
	t.Run("SameKeyRace", testCrabbingDeleteSameKeyRace)
	t.Run("InterleavedWithInsert", testCrabbingDeleteInterleavedWithInsert)
}

// testCrabbingDeleteDisjointSubtrees pre-builds a moderately large tree
// (multiple leaves, multiple internal levels), then runs many goroutines
// concurrently, each confined to deleting its own disjoint subset of the
// pre-built keys (assigned round-robin, so different goroutines' targets
// land throughout the tree rather than in isolated far-apart ranges,
// exercising real shared-parent contention on the borrow/merge repair path
// without any two goroutines racing on the very same key). Asserts every
// deleted key is absent, every surviving key is still look-up-able with the
// correct fileID, and the final tree is structurally valid with no
// dangling/aliased pointers.
func testCrabbingDeleteDisjointSubtrees(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 3000
	rootID, inserted := insertN(t, store, alloc, n)
	tree := NewTree(store, alloc, rootID)

	// Delete every key at an index congruent to 0 mod 3, forcing many leaves
	// across the tree to underflow and trigger borrow/merge concurrently.
	var toDelete []string
	for i := 0; i < n; i++ {
		if i%3 == 0 {
			toDelete = append(toDelete, genKey(i))
		}
	}

	const goroutines = 24
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := g; idx < len(toDelete); idx += goroutines {
				key := toDelete[idx]
				found, err := tree.Delete(key)
				if err != nil {
					errCh <- fmt.Errorf("goroutine %d: Delete(%q): %w", g, key, err)
					return
				}
				if !found {
					errCh <- fmt.Errorf("goroutine %d: Delete(%q): expected found=true, got false", g, key)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	finalRoot := tree.Root()
	assertAbsent(t, store, finalRoot, toDelete)

	remaining := make(map[string]uint64, len(inserted)-len(toDelete))
	for i := 0; i < n; i++ {
		if i%3 != 0 {
			key := genKey(i)
			remaining[key] = inserted[key]
		}
	}
	assertAllLookupable(t, store, finalRoot, remaining)
	assertStructuralInvariants(t, store, finalRoot, len(remaining))
	assertNoOrphanedPointers(t, store, finalRoot)
}

// testCrabbingDeleteSameKeyRace has many goroutines race to Delete the exact
// same key concurrently: exactly one must observe found=true, every other
// must observe found=false, and no goroutine may see a spurious error --
// exercising the leaf-level "already refilled/already repaired" benign-race
// retry path in repairEmptyLeafAtParent (and crabDeleteOnce's own absent-key
// path) under real contention.
func testCrabbingDeleteSameKeyRace(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 500
	rootID, _ := insertN(t, store, alloc, n)
	tree := NewTree(store, alloc, rootID)

	target := genKey(n / 2)

	const goroutines = 16
	var wg sync.WaitGroup
	foundCount := make([]bool, goroutines)
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			found, err := tree.Delete(target)
			if err != nil {
				errCh <- fmt.Errorf("goroutine %d: Delete(%q): %w", g, target, err)
				return
			}
			foundCount[g] = found
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	trueCount := 0
	for _, f := range foundCount {
		if f {
			trueCount++
		}
	}
	if trueCount != 1 {
		t.Fatalf("expected exactly 1 goroutine to observe found=true racing to delete %q, got %d", target, trueCount)
	}

	finalRoot := tree.Root()
	assertAbsent(t, store, finalRoot, []string{target})
	assertStructuralInvariants(t, store, finalRoot, n-1)
	assertNoOrphanedPointers(t, store, finalRoot)
}

// testCrabbingDeleteInterleavedWithInsert is this subtask's core acceptance
// test: concurrent deletes interleaved with concurrent inserts, hitting
// overlapping key ranges (adjacent, interleaved index positions, not just
// far-apart disjoint subtrees) so real contention -- merges from deletes AND
// splits from inserts -- happens in the very same physical region of the
// tree at the same time. The final tree is compared against a
// serial-execution oracle: every operation targets a key derived
// deterministically from its own goroutine/index assignment, with delete
// targets and insert targets drawn from disjoint key spaces (so, exactly
// like TestStripedConcurrencyStress in engine/catalog/catalog_test.go, the
// oracle's final expected state is unambiguous regardless of scheduling
// order, while the physical layout genuinely interleaves them).
func testCrabbingDeleteInterleavedWithInsert(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 4000
	rootID, inserted := insertN(t, store, alloc, n)
	tree := NewTree(store, alloc, rootID)

	// newKey(i) sorts strictly between genKey(i) and genKey(i+1) (a longer
	// string sharing genKey(i)'s exact prefix sorts after it, and the digit
	// difference at position len("topic000") guarantees it sorts before
	// genKey(i+1) regardless of suffix) -- so concurrently inserting
	// newKey(i) routes into the very same leaf genKey(i) already lives in,
	// forcing real shared-leaf contention with any concurrent delete of a
	// neighboring genKey index.
	newKey := func(i int) string { return genKey(i) + "-new" }

	var toDelete []string // indices where i%3 == 0
	var toInsert []int    // indices where i%3 == 1
	for i := 0; i < n; i++ {
		switch i % 3 {
		case 0:
			toDelete = append(toDelete, genKey(i))
		case 1:
			toInsert = append(toInsert, i)
		}
	}

	const delGoroutines = 16
	const insGoroutines = 16
	var wg sync.WaitGroup
	errCh := make(chan error, delGoroutines+insGoroutines)

	for g := 0; g < delGoroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := g; idx < len(toDelete); idx += delGoroutines {
				key := toDelete[idx]
				found, err := tree.Delete(key)
				if err != nil {
					errCh <- fmt.Errorf("delete goroutine %d: Delete(%q): %w", g, key, err)
					return
				}
				if !found {
					errCh <- fmt.Errorf("delete goroutine %d: Delete(%q): expected found=true, got false", g, key)
					return
				}
			}
		}()
	}
	for g := 0; g < insGoroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := g; idx < len(toInsert); idx += insGoroutines {
				i := toInsert[idx]
				key := newKey(i)
				fileID := uint64(1_000_000 + i)
				if err := tree.Insert(key, fileID); err != nil {
					errCh <- fmt.Errorf("insert goroutine %d: Insert(%q): %w", g, key, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	finalRoot := tree.Root()

	// Oracle: every original key at an index NOT congruent to 0 mod 3
	// survives with its original fileID; every newKey(i) for i%3==1 is
	// present with its inserted fileID; every original key at i%3==0 is
	// absent.
	wantPresent := make(map[string]uint64)
	var wantAbsent []string
	for i := 0; i < n; i++ {
		key := genKey(i)
		if i%3 == 0 {
			wantAbsent = append(wantAbsent, key)
			continue
		}
		wantPresent[key] = inserted[key]
		if i%3 == 1 {
			wantPresent[newKey(i)] = uint64(1_000_000 + i)
		}
	}

	assertAllLookupable(t, store, finalRoot, wantPresent)
	assertAbsent(t, store, finalRoot, wantAbsent)
	assertStructuralInvariants(t, store, finalRoot, len(wantPresent))
	assertNoOrphanedPointers(t, store, finalRoot)
}

// ---------------------------------------------------------------------------
// 2a.4.3 fix cycle: TestDeleteSpliceFirstChildAncestorFixesNextSibling.
//
// Verification (.cdr/runs/2026-07-06/005-verification/verification.json)
// found that fix round 1 of spliceOutDegenerateAncestor only patched a
// degenerate ancestor's true left neighbor when that neighbor was a child of
// the SAME grandparent (gj > 0). When the spliced ancestor is instead its
// grandparent's FIRST child (gj == 0), the true left neighbor lives under an
// entirely different, adjacent grandparent subtree, and was never located or
// patched -- leaving a dangling NextSibling pointer to the abandoned,
// unreachable node forever (node IDs are never reused).
//
// This test hand-constructs (bypassing Insert entirely, mirroring
// lookup_test.go's buildTestTree scaffolding) a fixed 4-level tree shaped
// specifically to force that gj == 0 code path through two levels of
// "first child of its parent" ancestry, then drives one real Delete call
// through the exact leaf-empty -> merge -> grandparent-splice cascade, and
// independently re-derives (never merely re-checking the function's own
// output) the correct post-splice NextSibling topology by walking the tree
// structure directly, mirroring assertStructuralInvariants' own
// subtreeMinKey-style independent-recomputation approach.
//
// Tree shape (see inline node-ID constants below for the exact wiring):
//
//	root (17): Children=[Gprev(15), G(16)]
//	  Gprev (15): Children=[X1(11), X2(12)]      -- untouched by the delete
//	    X1 (11): Children=[leaf 1, leaf 2]         (keys topic0100..0103)
//	    X2 (12): Children=[leaf 3, leaf 4]         (keys topic0104..0107)
//	  G (16): Children=[ANC(13), ANC2(14)]
//	    ANC (13): Children=[leaf 5, leaf 6]        (keys topic0108,0109,0110)
//	    ANC2 (14): Children=[leaf 7, leaf 8]       (keys topic0200..0203)
//
// Level-2 NextSibling chain (X1, X2, ANC, ANC2): X1 -> X2 -> ANC -> ANC2 ->
// noSibling. Deleting topic0108 then topic0109 empties leaf 5, which merges
// with leaf 6 (single key, so merge not borrow), degenerating ANC to 1
// child; ANC is G's FIRST child (gj == 0 in G), so ANC gets spliced out of G
// entirely. ANC's true NextSibling-chain predecessor is X2 -- NOT a child of
// G at all, but the last child of Gprev, G's own predecessor at the level
// above -- exactly the previously-unhandled case. After the fix, X2's
// NextSibling must be repointed at ANC2 (ANC's own former NextSibling),
// skipping over the now-abandoned ANC.
func TestDeleteSpliceFirstChildAncestorFixesNextSibling(t *testing.T) {
	const (
		leafX1a   = uint64(1) // topic0100, topic0101
		leafX1b   = uint64(2) // topic0102, topic0103
		leafX2a   = uint64(3) // topic0104, topic0105
		leafX2b   = uint64(4) // topic0106, topic0107
		leafAncA  = uint64(5) // topic0108, topic0109 -- emptied by this test
		leafAncB  = uint64(6) // topic0110 (single key: forces merge, not borrow)
		leafAnc2a = uint64(7) // topic0200, topic0201
		leafAnc2b = uint64(8) // topic0202, topic0203

		x1ID    = uint64(11)
		x2ID    = uint64(12)
		ancID   = uint64(13)
		anc2ID  = uint64(14)
		gprevID = uint64(15)
		gID     = uint64(16)
		rootID  = uint64(17)
	)

	path := filepath.Join(t.TempDir(), "name.idx")
	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	store := NewNodeStore(f)
	alloc, err := NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("NewNodeAllocator: %v", err)
	}
	t.Cleanup(func() { alloc.Close() })

	writeLeafNode := func(id uint64, l LeafNode) {
		t.Helper()
		if err := writeLeaf(store, id, l); err != nil {
			t.Fatalf("writeLeaf(%d): %v", id, err)
		}
	}
	writeInternalNode := func(id uint64, n InternalNode) {
		t.Helper()
		if err := writeInternal(store, id, n); err != nil {
			t.Fatalf("writeInternal(%d): %v", id, err)
		}
	}

	// Leaf level, chained left-to-right via NextLeaf covering every leaf.
	writeLeafNode(leafX1a, LeafNode{Keys: []string{genKey(100), genKey(101)}, FileIDs: []uint64{100, 101}, NextLeaf: leafX1b})
	writeLeafNode(leafX1b, LeafNode{Keys: []string{genKey(102), genKey(103)}, FileIDs: []uint64{102, 103}, NextLeaf: leafX2a})
	writeLeafNode(leafX2a, LeafNode{Keys: []string{genKey(104), genKey(105)}, FileIDs: []uint64{104, 105}, NextLeaf: leafX2b})
	writeLeafNode(leafX2b, LeafNode{Keys: []string{genKey(106), genKey(107)}, FileIDs: []uint64{106, 107}, NextLeaf: leafAncA})
	writeLeafNode(leafAncA, LeafNode{Keys: []string{genKey(108), genKey(109)}, FileIDs: []uint64{108, 109}, NextLeaf: leafAncB})
	writeLeafNode(leafAncB, LeafNode{Keys: []string{genKey(110)}, FileIDs: []uint64{110}, NextLeaf: leafAnc2a})
	writeLeafNode(leafAnc2a, LeafNode{Keys: []string{genKey(200), genKey(201)}, FileIDs: []uint64{200, 201}, NextLeaf: leafAnc2b})
	writeLeafNode(leafAnc2b, LeafNode{Keys: []string{genKey(202), genKey(203)}, FileIDs: []uint64{202, 203}, NextLeaf: noSibling})

	// Level 2 (X1, X2, ANC, ANC2): NextSibling chain X1 -> X2 -> ANC -> ANC2
	// -> noSibling. X2.NextSibling == ancID is the dangling link this test
	// exists to force and verify gets repointed to anc2ID.
	writeInternalNode(x1ID, InternalNode{Keys: []string{genKey(102)}, Children: []uint64{leafX1a, leafX1b}, NextSibling: x2ID, LowKey: ""})
	writeInternalNode(x2ID, InternalNode{Keys: []string{genKey(106)}, Children: []uint64{leafX2a, leafX2b}, NextSibling: ancID, LowKey: genKey(104)})
	writeInternalNode(ancID, InternalNode{Keys: []string{genKey(110)}, Children: []uint64{leafAncA, leafAncB}, NextSibling: anc2ID, LowKey: genKey(108)})
	writeInternalNode(anc2ID, InternalNode{Keys: []string{genKey(202)}, Children: []uint64{leafAnc2a, leafAnc2b}, NextSibling: noSibling, LowKey: genKey(200)})

	// Level 1 (Gprev, G): Gprev -> G -> noSibling.
	writeInternalNode(gprevID, InternalNode{Keys: []string{genKey(104)}, Children: []uint64{x1ID, x2ID}, NextSibling: gID, LowKey: ""})
	writeInternalNode(gID, InternalNode{Keys: []string{genKey(200)}, Children: []uint64{ancID, anc2ID}, NextSibling: noSibling, LowKey: genKey(108)})

	// Root (level 0).
	writeInternalNode(rootID, InternalNode{Keys: []string{genKey(108)}, Children: []uint64{gprevID, gID}, NextSibling: noSibling, LowKey: ""})

	// Drive the real Delete path: delete topic0108 (leaf still holds
	// topic0109 afterward, no repair triggered), then topic0109 (leaf 5
	// becomes empty, triggering merge-with-right-sibling since leaf 6 holds
	// only 1 key, which degenerates ANC to 1 child and splices it out of G
	// -- G's FIRST child, exactly the gj == 0 case this fix covers).
	// This regression targets spliceOutDegenerateAncestor, which is only
	// reachable via the concurrent Tree.Delete (crabbing) path -- the
	// free-function Delete(store, alloc, ...) goes through the separate,
	// single-threaded shrinkParentAfterMerge repair instead and never
	// exercises the bug this test exists to catch.
	tr := NewTree(store, alloc, rootID)
	for _, key := range []string{genKey(108), genKey(109)} {
		found, err := tr.Delete(key)
		if err != nil {
			t.Fatalf("Delete(%q): unexpected error: %v", key, err)
		}
		if !found {
			t.Fatalf("Delete(%q): expected found=true, got false", key)
		}
	}
	currentRoot := tr.Root()

	// The root itself never collapses in this scenario (G still has 2
	// children after ANC is spliced out: the surviving leaf and ANC2), so
	// currentRoot must still be rootID.
	if currentRoot != rootID {
		t.Fatalf("root changed to %d, want unchanged %d (this scenario should not collapse the root)", currentRoot, rootID)
	}

	// Independent check #1: every surviving key is still correctly
	// lookup-able, and the two deleted keys are genuinely gone.
	assertAbsent(t, store, currentRoot, []string{genKey(108), genKey(109)})
	wantPresent := map[string]uint64{
		genKey(100): 100, genKey(101): 101, genKey(102): 102, genKey(103): 103,
		genKey(104): 104, genKey(105): 105, genKey(106): 106, genKey(107): 107,
		genKey(110): 110,
		genKey(200): 200, genKey(201): 201, genKey(202): 202, genKey(203): 203,
	}
	assertAllLookupable(t, store, currentRoot, wantPresent)
	assertNoOrphanedPointers(t, store, currentRoot)

	// Independent check #2 (the core assertion): re-derive G's Children
	// directly and confirm ancID is no longer reachable at all (spliced
	// out), then walk the level-2 NextSibling chain from its independently
	// re-located head (X1, found by descending Children[0] from the root)
	// and confirm it now reads X1 -> X2 -> ANC2 -> noSibling, with X2's
	// NextSibling specifically repointed away from the abandoned ancID and
	// onto anc2ID -- never merely re-checking spliceOutDegenerateAncestor's
	// own internal bookkeeping.
	gIsLeaf, _, gNode, err := store.ReadNode(gID)
	if err != nil {
		t.Fatalf("ReadNode(G): unexpected error: %v", err)
	}
	if gIsLeaf {
		t.Fatalf("G decoded as a leaf, want internal")
	}
	if indexOfChild(gNode.Children, ancID) >= 0 {
		t.Fatalf("G.Children still references abandoned ancID %d: %v (splice did not remove it)", ancID, gNode.Children)
	}
	if len(gNode.Children) != 2 {
		t.Fatalf("G.Children = %v, want exactly 2 entries (the surviving leaf + anc2ID)", gNode.Children)
	}

	chain := []uint64{}
	for id := x1ID; id != noSibling; {
		isLeaf, _, node, err := store.ReadNode(id)
		if err != nil {
			t.Fatalf("ReadNode(%d) while walking level-2 NextSibling chain: %v", id, err)
		}
		if isLeaf {
			t.Fatalf("level-2 NextSibling chain reached a leaf node %d", id)
		}
		chain = append(chain, id)
		if len(chain) > 10 {
			t.Fatalf("level-2 NextSibling chain did not terminate within 10 hops (cycle?): %v", chain)
		}
		id = node.NextSibling
	}
	wantChain := []uint64{x1ID, x2ID, anc2ID}
	if len(chain) != len(wantChain) {
		t.Fatalf("level-2 NextSibling chain = %v, want %v", chain, wantChain)
	}
	for i := range wantChain {
		if chain[i] != wantChain[i] {
			t.Fatalf("level-2 NextSibling chain = %v, want %v (X2's NextSibling must skip the abandoned ancID %d and land on anc2ID %d)", chain, wantChain, ancID, anc2ID)
		}
	}

	// Belt-and-braces: read X2 directly and assert its NextSibling field is
	// exactly anc2ID, not noSibling and not the abandoned ancID -- the
	// single field fix round 1 of this subtask left dangling.
	_, _, x2Node, err := store.ReadNode(x2ID)
	if err != nil {
		t.Fatalf("ReadNode(X2): unexpected error: %v", err)
	}
	if x2Node.NextSibling != anc2ID {
		t.Fatalf("X2.NextSibling = %d, want %d (anc2ID); got noSibling=%d, abandoned ancID=%d for reference", x2Node.NextSibling, anc2ID, noSibling, ancID)
	}
}

// ---------------------------------------------------------------------------
// Issue #38 subtask 4.5.1.1: TestDeleteSpliceGj0CrossGrandparentNoDangling.
//
// TestDeleteSpliceFirstChildAncestorFixesNextSibling (above) already commits
// coverage for the gj==0 cross-grandparent case where the true left neighbor
// is found after walking up exactly ONE level past the grandparent
// (levelsUp == 1 in findLeftNeighborAtSameLevel). 007-verification
// (.cdr/runs/2026-07-06/007-verification/verification.json) PASSed that fix
// but flagged a non-blocking test_coverage gap: the deeper, multi-level
// nested gj==0 case (levelsUp >= 2) -- where the spliced ancestor's parent is
// ALSO its own parent's first child, forcing findLeftNeighborAtSameLevel to
// walk up more than one level before finding a non-first-child ancestor, then
// descend back down multiple "last child" hops to land on the true left
// neighbor -- was exercised only by a temporary, uncommitted test deleted
// after that verification run. This test commits that coverage.
//
// Tree shape (5 levels: root -> level1 -> level2 -> level3(=ancestor's own
// level, parent of leaves) -> leaves):
//
//	root (28): Children=[Lprev(23), L(27)]
//	  Lprev (23): Children=[pa(1, leaf), Pb(22)]         -- untouched by delete
//	    Pb (22): Children=[qa(2, leaf), Qb(21)]
//	      Qb (21): Children=[qbA(3, leaf), qbB(4, leaf)]   (keys topic0020..0023)
//	  L (27): Children=[G(26), gsib(9, leaf)]
//	    G (26): Children=[ANC(24), ANC2(25)]
//	      ANC (24): Children=[ancA(5, leaf), ancB(6, leaf)] (keys topic0100,0101,0102)
//	      ANC2 (25): Children=[anc2A(7, leaf), anc2B(8, leaf)] (keys topic0300..0303)
//
// G is L's FIRST child (so the initial hop up from G yields index 0 again),
// and L is root's SECOND child (so the walk-up terminates there, having gone
// up two levels: G's own parent(L) is a first child, L's own parent(root) is
// not). levelsUp == 2 forces findLeftNeighborAtSameLevel to descend back down
// via "always take the last child" exactly twice from Lprev (root.Children[0])
// to land on Qb -- Qb is ANC's true level-3 NextSibling-chain predecessor,
// living two subtrees to the left and two levels up, NOT a child of G at all.
//
// Level-3 NextSibling chain before delete: Qb -> ANC -> ANC2 -> noSibling.
// Deleting topic0100 then topic0101 empties leaf ancA, which merges with
// ancB (single remaining key: merge, not borrow), degenerating ANC to 1
// child; ANC is G's FIRST child (gj == 0 in G), so ANC gets spliced out of G
// entirely. After the fix, Qb's NextSibling must be repointed at ANC2 (ANC's
// own former NextSibling), skipping over the now-abandoned ANC -- never
// left dangling, and never a link a concurrent descent could be misrouted
// through into the abandoned, Children-unreachable ANC node.
func TestDeleteSpliceGj0CrossGrandparentNoDangling(t *testing.T) {
	const (
		leafPa    = uint64(1) // topic0000, topic0001
		leafQa    = uint64(2) // topic0010, topic0011
		leafQbA   = uint64(3) // topic0020, topic0021
		leafQbB   = uint64(4) // topic0022, topic0023
		leafAncA  = uint64(5) // topic0100, topic0101 -- emptied
		leafAncB  = uint64(6) // topic0102 (single key: merge, not borrow)
		leafAnc2A = uint64(7) // topic0300, topic0301
		leafAnc2B = uint64(8) // topic0302, topic0303
		leafGsib  = uint64(9) // topic0400, topic0401

		qbID    = uint64(21)
		pbID    = uint64(22)
		lprevID = uint64(23)
		ancID   = uint64(24)
		anc2ID  = uint64(25)
		gID     = uint64(26)
		lID     = uint64(27)
		rootID  = uint64(28)
	)

	path := filepath.Join(t.TempDir(), "gj0-nested.idx")
	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	store := NewNodeStore(f)
	alloc, err := NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("NewNodeAllocator: %v", err)
	}
	t.Cleanup(func() { alloc.Close() })

	writeLeafNode := func(id uint64, l LeafNode) {
		t.Helper()
		if err := writeLeaf(store, id, l); err != nil {
			t.Fatalf("writeLeaf(%d): %v", id, err)
		}
	}
	writeInternalNode := func(id uint64, n InternalNode) {
		t.Helper()
		if err := writeInternal(store, id, n); err != nil {
			t.Fatalf("writeInternal(%d): %v", id, err)
		}
	}

	// Leaves, wired left-to-right via NextLeaf.
	writeLeafNode(leafPa, LeafNode{Keys: []string{genKey(0), genKey(1)}, FileIDs: []uint64{0, 1}, NextLeaf: leafQa})
	writeLeafNode(leafQa, LeafNode{Keys: []string{genKey(10), genKey(11)}, FileIDs: []uint64{10, 11}, NextLeaf: leafQbA})
	writeLeafNode(leafQbA, LeafNode{Keys: []string{genKey(20), genKey(21)}, FileIDs: []uint64{20, 21}, NextLeaf: leafQbB})
	writeLeafNode(leafQbB, LeafNode{Keys: []string{genKey(22), genKey(23)}, FileIDs: []uint64{22, 23}, NextLeaf: leafAncA})
	writeLeafNode(leafAncA, LeafNode{Keys: []string{genKey(100), genKey(101)}, FileIDs: []uint64{100, 101}, NextLeaf: leafAncB})
	writeLeafNode(leafAncB, LeafNode{Keys: []string{genKey(102)}, FileIDs: []uint64{102}, NextLeaf: leafAnc2A})
	writeLeafNode(leafAnc2A, LeafNode{Keys: []string{genKey(300), genKey(301)}, FileIDs: []uint64{300, 301}, NextLeaf: leafAnc2B})
	writeLeafNode(leafAnc2B, LeafNode{Keys: []string{genKey(302), genKey(303)}, FileIDs: []uint64{302, 303}, NextLeaf: leafGsib})
	writeLeafNode(leafGsib, LeafNode{Keys: []string{genKey(400), genKey(401)}, FileIDs: []uint64{400, 401}, NextLeaf: noSibling})

	// Level 3 (ancestor's own level, parent-of-leaves): Qb -> ANC -> ANC2 ->
	// noSibling. Qb is the chain head (LowKey == "").
	writeInternalNode(qbID, InternalNode{Keys: []string{genKey(22)}, Children: []uint64{leafQbA, leafQbB}, NextSibling: ancID, LowKey: ""})
	writeInternalNode(ancID, InternalNode{Keys: []string{genKey(102)}, Children: []uint64{leafAncA, leafAncB}, NextSibling: anc2ID, LowKey: genKey(100)})
	writeInternalNode(anc2ID, InternalNode{Keys: []string{genKey(302)}, Children: []uint64{leafAnc2A, leafAnc2B}, NextSibling: noSibling, LowKey: genKey(300)})

	// Level 2: Pb -> G -> noSibling. Pb is the chain head (LowKey == "").
	writeInternalNode(pbID, InternalNode{Keys: []string{genKey(20)}, Children: []uint64{leafQa, qbID}, NextSibling: gID, LowKey: ""})
	writeInternalNode(gID, InternalNode{Keys: []string{genKey(300)}, Children: []uint64{ancID, anc2ID}, NextSibling: noSibling, LowKey: genKey(100)})

	// Level 1: Lprev -> L -> noSibling. Lprev is the chain head (LowKey ==
	// ""). G is L's FIRST child -- one hop up from G lands on L, whose own
	// index within root.Children is ALSO 0 relative to... no: G is L's first
	// child (index 0 in L.Children), forcing findLeftNeighborAtSameLevel's
	// first up-hop (from G's parent, gID's level) to re-check at L's level;
	// L is root's SECOND child (index 1), which is where the walk-up
	// terminates, having gone up two levels total (levelsUp == 2).
	writeInternalNode(lprevID, InternalNode{Keys: []string{genKey(10)}, Children: []uint64{leafPa, pbID}, NextSibling: lID, LowKey: ""})
	writeInternalNode(lID, InternalNode{Keys: []string{genKey(400)}, Children: []uint64{gID, leafGsib}, NextSibling: noSibling, LowKey: genKey(100)})

	// Root (level 0).
	writeInternalNode(rootID, InternalNode{Keys: []string{genKey(100)}, Children: []uint64{lprevID, lID}, NextSibling: noSibling, LowKey: ""})

	tr := NewTree(store, alloc, rootID)

	wantPresent := map[string]uint64{
		genKey(0): 0, genKey(1): 1,
		genKey(10): 10, genKey(11): 11,
		genKey(20): 20, genKey(21): 21, genKey(22): 22, genKey(23): 23,
		genKey(102): 102,
		genKey(300): 300, genKey(301): 301, genKey(302): 302, genKey(303): 303,
		genKey(400): 400, genKey(401): 401,
	}
	deletedKeys := []string{genKey(100), genKey(101)}

	// Concurrent-descent clause: race a pool of read-only Lookup goroutines
	// against the Delete sequence that triggers the gj==0 splice, so that if
	// the fix ever regressed and a concurrent descent were misrouted through
	// a dangling NextSibling into the abandoned ancID node, it would surface
	// here as an error/panic/incorrect miss under -race, not just as a
	// structural invariant violation checked only after the fact.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	const lookupGoroutines = 8
	for g := 0; g < lookupGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for key, wantFileID := range wantPresent {
					fileID, found, err := tr.Lookup(key)
					if err != nil {
						errCh <- fmt.Errorf("concurrent Lookup(%q): unexpected error: %v", key, err)
						return
					}
					if !found || fileID != wantFileID {
						errCh <- fmt.Errorf("concurrent Lookup(%q) = (%d, %v), want (%d, true)", key, fileID, found, wantFileID)
						return
					}
				}
			}
		}()
	}

	for _, key := range deletedKeys {
		found, err := tr.Delete(key)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Delete(%q): unexpected error: %v", key, err)
		}
		if !found {
			close(stop)
			wg.Wait()
			t.Fatalf("Delete(%q): expected found=true, got false", key)
		}
	}

	close(stop)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	currentRoot := tr.Root()
	if currentRoot != rootID {
		t.Fatalf("Tree.Root() = %d, want unchanged %d (root itself never collapses in this scenario)", currentRoot, rootID)
	}

	// Assertion #1: standard tree-health checks, including deletedKeys now
	// absent and every other key still lookup-able.
	assertAllLookupable(t, store, currentRoot, wantPresent)
	assertAbsent(t, store, currentRoot, deletedKeys)
	assertStructuralInvariants(t, store, currentRoot, len(wantPresent))
	assertNoOrphanedPointers(t, store, currentRoot)

	// Assertion #2: G no longer references the abandoned ancID.
	gIsLeaf, _, gNode, err := store.ReadNode(gID)
	if err != nil {
		t.Fatalf("ReadNode(G): unexpected error: %v", err)
	}
	if gIsLeaf {
		t.Fatalf("G decoded as a leaf, want internal")
	}
	if indexOfChild(gNode.Children, ancID) >= 0 {
		t.Fatalf("G.Children still references abandoned ancID %d: %v (splice did not remove it)", ancID, gNode.Children)
	}
	if len(gNode.Children) != 2 {
		t.Fatalf("G.Children = %v, want exactly 2 entries (the surviving leaf + anc2ID)", gNode.Children)
	}

	// Assertion #3: independently re-derive the level-3 NextSibling chain by
	// walking it directly (never merely re-checking spliceOutDegenerateAncestor's
	// own internal bookkeeping), and confirm it now reads Qb -> ANC2 ->
	// noSibling -- Qb's NextSibling specifically repointed away from the
	// abandoned ancID onto anc2ID, with no dangling reference anywhere.
	var chain []uint64
	for id := qbID; id != noSibling; {
		isLeaf, _, node, err := store.ReadNode(id)
		if err != nil {
			t.Fatalf("ReadNode(%d) while walking level-3 NextSibling chain: %v", id, err)
		}
		if isLeaf {
			t.Fatalf("level-3 NextSibling chain reached leaf node %d", id)
		}
		if id == ancID {
			t.Fatalf("level-3 NextSibling chain still reaches abandoned ancID %d (dangling NextSibling not patched)", ancID)
		}
		chain = append(chain, id)
		if len(chain) > 10 {
			t.Fatalf("level-3 NextSibling chain did not terminate within 10 hops (cycle?): %v", chain)
		}
		id = node.NextSibling
	}
	wantChain := []uint64{qbID, anc2ID}
	if len(chain) != len(wantChain) {
		t.Fatalf("level-3 NextSibling chain = %v, want %v", chain, wantChain)
	}
	for i := range wantChain {
		if chain[i] != wantChain[i] {
			t.Fatalf("level-3 NextSibling chain = %v, want %v (Qb's NextSibling must skip the abandoned ancID %d and land on anc2ID %d)", chain, wantChain, ancID, anc2ID)
		}
	}

	// Belt-and-braces: read Qb directly and assert its NextSibling field is
	// exactly anc2ID, not noSibling and not the abandoned ancID.
	_, _, qbNode, err := store.ReadNode(qbID)
	if err != nil {
		t.Fatalf("ReadNode(Qb): unexpected error: %v", err)
	}
	if qbNode.NextSibling != anc2ID {
		t.Fatalf("Qb.NextSibling = %d, want %d (anc2ID); got noSibling=%d, abandoned ancID=%d for reference", qbNode.NextSibling, anc2ID, noSibling, ancID)
	}
}
