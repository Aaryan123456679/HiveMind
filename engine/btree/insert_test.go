package btree

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestStoreAndAllocator opens a fresh, isolated (t.TempDir()) index file
// and wraps it in a NodeStore + NodeAllocator, ready for Insert calls. This is
// the real production path (NodeStore/NodeAllocator/Insert) -- NOT
// lookup_test.go's buildTestTree scaffolding, which this subtask's test spec
// explicitly must not reuse.
func newTestStoreAndAllocator(t *testing.T) (*NodeStore, *NodeAllocator) {
	t.Helper()

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

	return store, alloc
}

// TestInsertEmptyTree covers the empty-tree bootstrap case: a single insert
// into a brand-new tree (rootNodeID == reservedNodeID) allocates a leaf and
// makes it the root, and the inserted key is immediately lookup-able.
func TestInsertEmptyTree(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	rootID, err := Insert(store, alloc, reservedNodeID, "auth/login", 101)
	if err != nil {
		t.Fatalf("Insert: unexpected error: %v", err)
	}
	if rootID == reservedNodeID {
		t.Fatalf("Insert: returned reservedNodeID as new root, want a real node ID")
	}

	fileID, found, err := Lookup(store, rootID, "auth/login")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if !found || fileID != 101 {
		t.Fatalf("Lookup(auth/login) = (%d, %v), want (101, true)", fileID, found)
	}

	_, found, err = Lookup(store, rootID, "auth/logout")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("Lookup(auth/logout) found=true, want false (never inserted)")
	}
}

// TestInsertUpsert covers re-inserting an already-present key: it should
// update the fileID in place without changing the root or the tree shape.
func TestInsertUpsert(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	rootID, err := Insert(store, alloc, reservedNodeID, "auth/login", 101)
	if err != nil {
		t.Fatalf("Insert: unexpected error: %v", err)
	}

	rootID2, err := Insert(store, alloc, rootID, "auth/login", 999)
	if err != nil {
		t.Fatalf("Insert (update): unexpected error: %v", err)
	}
	if rootID2 != rootID {
		t.Fatalf("Insert (update): root changed from %d to %d, want unchanged", rootID, rootID2)
	}

	fileID, found, err := Lookup(store, rootID2, "auth/login")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if !found || fileID != 999 {
		t.Fatalf("Lookup(auth/login) after update = (%d, %v), want (999, true)", fileID, found)
	}
}

// genKey deterministically produces a sortable, realistic-looking topic-path
// key for index i, e.g. "topic0007/page".
func genKey(i int) string {
	return fmt.Sprintf("topic%04d/page", i)
}

// insertN inserts n sequential keys (genKey(0)..genKey(n-1), each with fileID
// = i+1) into the tree via the real Insert path only, returning the final
// root ID and the set of (key, fileID) pairs inserted for later verification.
func insertN(t *testing.T, store *NodeStore, alloc *NodeAllocator, n int) (rootID uint64, inserted map[string]uint64) {
	t.Helper()

	inserted = make(map[string]uint64, n)
	rootID = reservedNodeID
	for i := 0; i < n; i++ {
		key := genKey(i)
		fileID := uint64(i + 1)
		var err error
		rootID, err = Insert(store, alloc, rootID, key, fileID)
		if err != nil {
			t.Fatalf("Insert(%q): unexpected error: %v", key, err)
		}
		inserted[key] = fileID
	}
	return rootID, inserted
}

// assertAllLookupable verifies every key in inserted is found via Lookup with
// the correct fileID, and a handful of never-inserted keys are correctly
// reported absent.
func assertAllLookupable(t *testing.T, store *NodeStore, rootID uint64, inserted map[string]uint64) {
	t.Helper()

	for key, wantFileID := range inserted {
		fileID, found, err := Lookup(store, rootID, key)
		if err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", key, err)
		}
		if !found {
			t.Fatalf("Lookup(%q): expected found=true, got false", key)
		}
		if fileID != wantFileID {
			t.Fatalf("Lookup(%q) = %d, want %d", key, fileID, wantFileID)
		}
	}

	neverInserted := []string{"zzz-not-a-topic/page", "aaa-not-a-topic/page", "topic9999999/page"}
	for _, key := range neverInserted {
		if _, ok := inserted[key]; ok {
			continue
		}
		fileID, found, err := Lookup(store, rootID, key)
		if err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", key, err)
		}
		if found {
			t.Fatalf("Lookup(%q): expected found=false, got true (fileID=%d)", key, fileID)
		}
	}
}

// assertStructuralInvariants walks the whole tree from rootID and asserts:
//   - every internal node's Keys are sorted ascending
//   - every internal node has len(Children) == len(Keys)+1 (correct fanout)
//   - the leaf level, followed left-to-right via NextLeaf starting from the
//     tree's leftmost leaf, yields keys in globally sorted order and visits
//     every key exactly once
//   - at every internal level, NextSibling forms exactly one connected,
//     acyclic, strictly-increasing (by subtree minimum key) chain covering
//     every internal node at that depth, with exactly one chain head (the
//     node with LowKey == "") and the chain terminating at noSibling
//   - every internal node's LowKey is a valid (never-exceeded) lower bound
//     for the minimum key reachable in its own subtree, except the chain
//     head at each level (LowKey == "")
//
// The last two points close the gap flagged by 2a.4.2's fix regression
// (GitHub issue #9): the original assertStructuralInvariants only checked
// leaf-level NextLeaf/sorted-keys, so it could not have caught -- and would
// not catch a future regression of -- findParent's internal-level move-right
// and LowKey-based routing.
//
// LowKey is checked as "<= actual subtree min", not "== actual subtree
// min" (2a.4.5 fix): per InternalNode.LowKey's doc comment (node.go),
// LowKey is fixed forever once a node is created by a split and is never
// revised afterward, whereas the node's true subtree minimum can move
// strictly higher over time as Delete removes that subtree's own
// leftmost keys. That drift is intentional and harmless for routing
// (crabInsert/crabDelete/Lookup's move-right peeks only ever need LowKey
// to be a safe, non-exceeded lower bound, never an exact tracker; see
// insert.go/delete.go/lookup.go's identical peek logic) -- only a LowKey
// that is GREATER than the true subtree minimum would indicate genuine
// misrouted content and is still treated as a hard failure below.
func assertStructuralInvariants(t *testing.T, store *NodeStore, rootID uint64, wantKeyCount int) {
	t.Helper()

	// subtreeMinKey returns the smallest key reachable anywhere within
	// nodeID's subtree (descending via Children[0] for internal nodes, or
	// Keys[0] for a leaf), and whether one could be determined at all. A
	// leftmost leaf can legitimately be transiently empty in some Delete
	// repair shapes (out of this insert-focused invariant's scope -- see
	// Delete's own dedicated invariant checks), so callers must treat
	// ok == false as "cannot verify, skip this check" rather than a failure.
	var subtreeMinKey func(nodeID uint64) (key string, ok bool)
	subtreeMinKey = func(nodeID uint64) (string, bool) {
		isLeaf, leaf, internal, err := store.ReadNode(nodeID)
		if err != nil {
			t.Fatalf("ReadNode(%d): unexpected error: %v", nodeID, err)
		}
		if isLeaf {
			if len(leaf.Keys) == 0 {
				return "", false
			}
			return leaf.Keys[0], true
		}
		return subtreeMinKey(internal.Children[0])
	}

	// byLevel collects every internal node ID seen during the recursive
	// walk below, indexed by depth from rootID (0 == rootID's own level),
	// so the NextSibling-chain check below can be done per level.
	byLevel := make(map[int][]uint64)

	// Recursively validate every internal node's invariants (sorted keys,
	// correct fanout: len(Children) == len(Keys)+1, and LowKey correctness).
	var validate func(nodeID uint64, depth int)
	validate = func(nodeID uint64, depth int) {
		isLeaf, _, internal, err := store.ReadNode(nodeID)
		if err != nil {
			t.Fatalf("ReadNode(%d): unexpected error: %v", nodeID, err)
		}
		if isLeaf {
			return
		}
		byLevel[depth] = append(byLevel[depth], nodeID)

		if len(internal.Children) != len(internal.Keys)+1 {
			t.Fatalf("internal node %d: len(Children)=%d, want len(Keys)+1=%d", nodeID, len(internal.Children), len(internal.Keys)+1)
		}
		if !sort.StringsAreSorted(internal.Keys) {
			t.Fatalf("internal node %d: Keys not sorted ascending: %v", nodeID, internal.Keys)
		}
		if internal.LowKey != "" {
			if actualMin, ok := subtreeMinKey(nodeID); ok && internal.LowKey > actualMin {
				t.Fatalf("internal node %d: LowKey = %q exceeds its own subtree's actual minimum key %q (LowKey must never be greater than the true subtree minimum)", nodeID, internal.LowKey, actualMin)
			}
		}
		for _, child := range internal.Children {
			validate(child, depth+1)
		}
	}
	validate(rootID, 0)

	// Per internal level, NextSibling must form exactly one connected,
	// acyclic chain -- starting at the single node with LowKey == "" (the
	// level's head) and terminating at noSibling -- that visits every node
	// collected for that level in strictly increasing subtree-min-key order.
	for depth, nodeIDs := range byLevel {
		var head uint64
		heads := 0
		for _, id := range nodeIDs {
			_, _, internal, err := store.ReadNode(id)
			if err != nil {
				t.Fatalf("ReadNode(%d): unexpected error: %v", id, err)
			}
			if internal.LowKey == "" {
				head = id
				heads++
			}
		}
		if heads != 1 {
			t.Fatalf("internal level %d: found %d nodes with LowKey==\"\" (want exactly 1 chain head) among %v", depth, heads, nodeIDs)
		}

		visited := make(map[uint64]bool, len(nodeIDs))
		var lastMin string
		id := head
		for {
			if visited[id] {
				t.Fatalf("internal level %d: NextSibling chain revisited node %d (cycle)", depth, id)
			}
			visited[id] = true
			_, _, internal, err := store.ReadNode(id)
			if err != nil {
				t.Fatalf("ReadNode(%d): unexpected error: %v", id, err)
			}
			if min, ok := subtreeMinKey(id); ok {
				if lastMin != "" && min <= lastMin {
					t.Fatalf("internal level %d: NextSibling chain not strictly increasing at node %d (min %q <= previous %q)", depth, id, min, lastMin)
				}
				lastMin = min
			}
			if internal.NextSibling == noSibling {
				break
			}
			id = internal.NextSibling
		}
		if len(visited) != len(nodeIDs) {
			t.Fatalf("internal level %d: NextSibling chain visited %d nodes, want %d (chain does not cover every node collected at this level, or covers nodes from another level)", depth, len(visited), len(nodeIDs))
		}
	}

	// Descend to the leftmost leaf by always following child 0.
	leftmostLeaf := rootID
	for {
		isLeaf, _, internal, err := store.ReadNode(leftmostLeaf)
		if err != nil {
			t.Fatalf("ReadNode(%d): unexpected error: %v", leftmostLeaf, err)
		}
		if isLeaf {
			break
		}
		leftmostLeaf = internal.Children[0]
	}

	var allKeys []string
	seen := 0
	for id := leftmostLeaf; id != noSibling; {
		isLeaf, leaf, _, err := store.ReadNode(id)
		if err != nil {
			t.Fatalf("ReadNode(%d): unexpected error: %v", id, err)
		}
		if !isLeaf {
			t.Fatalf("NextLeaf chain led to non-leaf node %d", id)
		}
		if !sort.StringsAreSorted(leaf.Keys) {
			t.Fatalf("leaf node %d: Keys not sorted ascending: %v", id, leaf.Keys)
		}
		allKeys = append(allKeys, leaf.Keys...)
		seen += len(leaf.Keys)
		id = leaf.NextLeaf
	}

	if seen != wantKeyCount {
		t.Fatalf("NextLeaf chain visited %d keys, want %d", seen, wantKeyCount)
	}
	if !sort.StringsAreSorted(allKeys) {
		t.Fatalf("global key order across leaf chain not sorted ascending: %v", allKeys)
	}
}

// TestInsertLeafSplit inserts enough sequential keys to force at least one
// leaf split (a single 4096-byte NodeSize leaf holds well under 100 short
// keys of this shape), then verifies every inserted key is lookup-able via
// the real Insert/Lookup path and that structural invariants hold.
func TestInsertLeafSplit(t *testing.T) {
	testInsertLeafSplit(t)
}

// testInsertLeafSplit holds the actual leaf-split assertions and is shared
// by TestInsertLeafSplit and TestInsertSplit (the latter runs it as a
// subtest so that `go test -run TestInsertSplit` exercises real split-path
// coverage, per the acceptance test spec in subtask 1.2.3).
func testInsertLeafSplit(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	// A single 4096-byte NodeSize leaf holds roughly (NodeSize-offBody)/
	// (2+len(key)+8) keys of this shape (~14-byte keys -> ~24 bytes/entry, so
	// ~170 keys/leaf); 250 sequential inserts reliably forces at least one
	// leaf split without needing thousands of inserts.
	const n = 250
	rootID, inserted := insertN(t, store, alloc, n)

	isLeaf, _, _, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf {
		t.Fatalf("root is still a leaf after %d inserts, want at least one leaf split to have occurred (root promoted to internal)", n)
	}

	assertAllLookupable(t, store, rootID, inserted)
	assertStructuralInvariants(t, store, rootID, n)
}

// TestInsertInternalSplit inserts enough sequential keys to force multiple
// levels of splitting -- not just a leaf split but an internal-node split too
// -- producing an internal node with >= 2 separator keys. This closes the gap
// flagged by 1.2.2's verification (internal nodes with >= 2 keys were never
// exercised because lookup_test.go's buildTestTree scaffolding only built
// single-key internal nodes).
func TestInsertInternalSplit(t *testing.T) {
	testInsertInternalSplit(t)
}

// testInsertInternalSplit holds the actual internal-split assertions and is
// shared by TestInsertInternalSplit and TestInsertSplit (the latter runs it
// as a subtest so that `go test -run TestInsertSplit` exercises real
// split-path coverage, per the acceptance test spec in subtask 1.2.3).
func testInsertInternalSplit(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 2000
	rootID, inserted := insertN(t, store, alloc, n)

	isLeaf, _, rootInternal, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf {
		t.Fatalf("root is still a leaf after %d inserts, want an internal root", n)
	}

	// Find at least one internal node (root or below) with >= 2 separator
	// keys, closing 1.2.2's flagged gap.
	foundMultiKeyInternal := len(rootInternal.Keys) >= 2
	if !foundMultiKeyInternal {
		var walk func(nodeID uint64) bool
		walk = func(nodeID uint64) bool {
			isLeaf, _, internal, err := store.ReadNode(nodeID)
			if err != nil {
				t.Fatalf("ReadNode(%d): unexpected error: %v", nodeID, err)
			}
			if isLeaf {
				return false
			}
			if len(internal.Keys) >= 2 {
				return true
			}
			for _, child := range internal.Children {
				if walk(child) {
					return true
				}
			}
			return false
		}
		foundMultiKeyInternal = walk(rootID)
	}
	if !foundMultiKeyInternal {
		t.Fatalf("no internal node with >= 2 separator keys found after %d inserts, want at least one (closing 1.2.2's flagged gap)", n)
	}

	assertAllLookupable(t, store, rootID, inserted)
	assertStructuralInvariants(t, store, rootID, n)
}

// TestInsertSplit is the acceptance-test entry point named in GitHub issue
// #2's literal spec for subtask 1.2.3 (`go test ./engine/btree/... -run
// TestInsertSplit`). It exercises both the leaf-split and internal-split
// scenarios as subtests, reusing the same assertions as TestInsertLeafSplit
// and TestInsertInternalSplit so `-run TestInsertSplit` actually runs real
// split-path assertions instead of matching zero tests.
func TestInsertSplit(t *testing.T) {
	t.Run("LeafSplit", testInsertLeafSplit)
	t.Run("InternalSplit", testInsertInternalSplit)
}

// TestInsertOutOfOrder inserts keys in a shuffled (non-sequential) order to
// exercise splitting when new keys land in the middle of existing leaves/
// internal nodes, not just at the tail.
func TestInsertOutOfOrder(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 300
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	// Deterministic pseudo-shuffle: reverse odd/even interleave, avoids
	// depending on math/rand's seeding behavior across Go versions.
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		if i%2 == 0 {
			order[i], order[j] = order[j], order[i]
		}
	}

	rootID := uint64(reservedNodeID)
	inserted := make(map[string]uint64, n)
	for _, i := range order {
		key := genKey(i)
		fileID := uint64(i + 1)
		var err error
		rootID, err = Insert(store, alloc, rootID, key, fileID)
		if err != nil {
			t.Fatalf("Insert(%q): unexpected error: %v", key, err)
		}
		inserted[key] = fileID
	}

	assertAllLookupable(t, store, rootID, inserted)
	assertStructuralInvariants(t, store, rootID, n)
}

// TestCrabbingInsert is the acceptance-test entry point named in this
// subtask's (2a.4.2) literal test spec (`go test ./engine/btree/... -race
// -run TestCrabbingInsert`). It exercises the concurrency-safe Tree.Insert
// path (not the single-threaded free Insert function) via two subtests:
// disjoint far-apart key ranges (should proceed without any writer ever
// blocking on another writer's unrelated subtree) and a heavily overlapping
// key range (forcing real lock contention and, with enough concurrent
// writers, concurrent leaf/internal/root splits).
func TestCrabbingInsert(t *testing.T) {
	t.Run("DisjointSubtrees", testCrabbingInsertDisjointSubtrees)
	t.Run("OverlappingSubtree", testCrabbingInsertOverlappingSubtree)
	t.Run("DeepOverlappingSubtree", testCrabbingInsertDeepOverlappingSubtree)
	t.Run("VeryDeepOverlappingSubtree", testCrabbingInsertVeryDeepOverlappingSubtree)
}

// testCrabbingInsertDisjointSubtrees pre-builds a moderately large tree
// (multiple leaves, at least one internal split) single-threaded via the
// existing free Insert, wraps it in a Tree, then runs many goroutines
// concurrently, each confined to its own far-apart, non-overlapping key
// range (so each goroutine's descent should touch entirely different
// leaves/ancestors from every other goroutine's, once below the shared
// root). Asserts every inserted key -- both the pre-built ones and the
// concurrently-inserted ones -- is look-up-able afterward with the correct
// fileID, and that the final tree is structurally valid.
func testCrabbingInsertDisjointSubtrees(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const prebuilt = 2000
	rootID, inserted := insertN(t, store, alloc, prebuilt)

	tree := NewTree(store, alloc, rootID)

	const goroutines = 40
	const perGoroutine = 50
	// Each goroutine g owns the disjoint key range
	// [prebuilt + g*rangeWidth, prebuilt + g*rangeWidth + perGoroutine),
	// with a wide gap between ranges so different goroutines' keys land in
	// far-apart, non-overlapping leaves.
	const rangeWidth = 1000

	var mu sync.Mutex // guards `inserted` only; Tree.Insert itself needs no external synchronization
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			base := prebuilt + g*rangeWidth
			for i := 0; i < perGoroutine; i++ {
				idx := base + i
				key := genKey(idx)
				fileID := uint64(idx + 1)
				if err := tree.Insert(key, fileID); err != nil {
					errCh <- fmt.Errorf("goroutine %d: Insert(%q): %w", g, key, err)
					return
				}
				mu.Lock()
				inserted[key] = fileID
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	finalRoot := tree.Root()
	assertAllLookupable(t, store, finalRoot, inserted)
	assertStructuralInvariants(t, store, finalRoot, len(inserted))
}

// testCrabbingInsertOverlappingSubtree starts from an EMPTY tree and runs
// many goroutines whose keys are tightly interleaved (goroutine g inserts
// every key congruent to g modulo the goroutine count), so essentially
// every goroutine routes through the same shared root and, for a long
// stretch of the tree's growth, the same shared internal nodes and leaves --
// forcing real lock contention and very likely multiple concurrent leaf,
// internal, and root splits. Asserts every key lands correctly exactly
// once and the final tree is structurally valid.
func testCrabbingInsertOverlappingSubtree(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)
	tree := NewTree(store, alloc, reservedNodeID)

	const goroutines = 30
	const perGoroutine = 60
	const n = goroutines * perGoroutine

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := g; idx < n; idx += goroutines {
				key := genKey(idx)
				fileID := uint64(idx + 1)
				if err := tree.Insert(key, fileID); err != nil {
					errCh <- fmt.Errorf("goroutine %d: Insert(%q): %w", g, key, err)
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

	inserted := make(map[string]uint64, n)
	for idx := 0; idx < n; idx++ {
		inserted[genKey(idx)] = uint64(idx + 1)
	}

	finalRoot := tree.Root()
	assertAllLookupable(t, store, finalRoot, inserted)
	assertStructuralInvariants(t, store, finalRoot, n)

	// Sanity check that this scenario actually exercised concurrent splits,
	// not just a trivially small single-leaf tree: the root must have been
	// promoted to internal at least once.
	isLeaf, _, _, err := store.ReadNode(finalRoot)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf {
		t.Fatalf("root is still a leaf after %d concurrent inserts across %d goroutines, want at least one split to have occurred", n, goroutines)
	}
}

// testCrabbingInsertDeepOverlappingSubtree is testCrabbingInsertOverlappingSubtree
// scaled up (64 goroutines, ~30,000 total keys, same tightly-interleaved
// striped assignment) to a scale confirmed to reliably force the tree to at
// least 2 internal levels under real concurrency -- i.e. to actually
// exercise propagate's "ancestor overflowed, split it too" branch and
// findParent's internal-level move-right/leaf-chain-recovery logic, which
// testCrabbingInsertOverlappingSubtree's much shallower (single
// internal-level) regime never reaches.
//
// This closes the gap identified in the 2a.4.2 fix regression (GitHub issue
// #9): the original TestCrabbingInsert never grew past 1 internal level
// (fanout ~150-170 per node vs. 1800 total keys), so it could not have
// caught -- and could not catch a future regression of -- findParent's
// recovery path for a childID that is itself several splits ahead of its
// ancestor's last-linked Children entry. Without the findParent fix, this
// subtest reliably reproduces
// "btree: internal invariant violated: findParent reached leaf ... while
// searching for the current parent of ..." within a small number of runs.
func testCrabbingInsertDeepOverlappingSubtree(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)
	tree := NewTree(store, alloc, reservedNodeID)

	const goroutines = 64
	const perGoroutine = 470 // ~30,080 keys total
	const n = goroutines * perGoroutine

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := g; idx < n; idx += goroutines {
				key := genKey(idx)
				fileID := uint64(idx + 1)
				if err := tree.Insert(key, fileID); err != nil {
					errCh <- fmt.Errorf("goroutine %d: Insert(%q): %w", g, key, err)
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

	inserted := make(map[string]uint64, n)
	for idx := 0; idx < n; idx++ {
		inserted[genKey(idx)] = uint64(idx + 1)
	}

	finalRoot := tree.Root()
	assertAllLookupable(t, store, finalRoot, inserted)
	assertStructuralInvariants(t, store, finalRoot, n)

	// Sanity check that this scenario actually reached depth >= 2 (root ->
	// internal -> internal -> leaf), not just the single-internal-level
	// depth the pre-existing OverlappingSubtree subtest already covers.
	isLeaf, _, internal, err := store.ReadNode(finalRoot)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf {
		t.Fatalf("root is still a leaf after %d concurrent inserts across %d goroutines", n, goroutines)
	}
	childIsLeaf, _, _, err := store.ReadNode(internal.Children[0])
	if err != nil {
		t.Fatalf("ReadNode(root's first child): unexpected error: %v", err)
	}
	if childIsLeaf {
		t.Fatalf("tree only reached depth 1 (root -> leaves) after %d concurrent inserts across %d goroutines, want depth >= 2 (root -> internal -> ... -> leaves) to actually exercise findParent's internal-level recovery path", n, goroutines)
	}
}

// testCrabbingInsertVeryDeepOverlappingSubtree is testCrabbingInsertDeepOverlappingSubtree
// scaled up further still (160 goroutines, ~80,000 keys) to close the gap
// identified in the 2a.4.2 fix round 2 regression (GitHub issue #9): a
// distinct, more severe silent-data-loss bug (a previously-inserted key
// becomes unfindable via Lookup afterward, with no error surfaced anywhere)
// in Tree.propagate's promoted-key insertion position, caused by two
// children of the same parent splitting and being promoted concurrently
// using a stale positional index rather than promotedKey's actual sorted
// position. See Tree.propagate's insertion-position comment for the full
// race trace.
//
// Documented tradeoff (balance of runtime vs. reliability): this specific
// race was empirically confirmed to reproduce at only ~8.6% per run (3/35)
// at this 160-goroutine/80,000-key scale under -race, and was NOT
// reproduced at all in 40/40 runs at testCrabbingInsertDeepOverlappingSubtree's
// smaller 64-goroutine/30,080-key scale. That means:
//   - This subtest, run once per `go test` invocation (as CI normally does),
//     has only a modest chance of catching a future regression of this
//     specific race in any single run -- it is not a reliable single-shot
//     regression guard the way the other subtests in this file are.
//   - Running it enough times to get high-confidence detection (e.g. the
//     40+ repeated runs used to validate the fix during this fix cycle)
//     costs minutes of wall-clock time under -race, which is not practical
//     to pay on every CI run.
//   - It is still included once, at this scale, because: (a) it is the
//     smallest scale empirically observed to reproduce this exact bug at
//     all, so it is the best available committable regression signal for
//     it; (b) assertStructuralInvariants' sorted-Keys check (exercised here
//     the same as every other subtest) means any recurrence that IS caught
//     manifests as a clear, specific failure rather than a flaky timeout;
//     and (c) combined with repeated manual/adversarial runs during future
//     verification passes of any change touching Tree.propagate (as this
//     fix cycle itself did), the combination of "always run once in CI" +
//     "run adversarially many times whenever propagate changes" gives
//     reasonable coverage without permanently taxing every CI run with a
//     minutes-long, mostly-redundant repeated-run test.
func testCrabbingInsertVeryDeepOverlappingSubtree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 160-goroutine/80,000-key concurrent stress subtest in -short mode")
	}

	store, alloc := newTestStoreAndAllocator(t)
	tree := NewTree(store, alloc, reservedNodeID)

	const goroutines = 160
	const perGoroutine = 500 // 80,000 keys total
	const n = goroutines * perGoroutine

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := g; idx < n; idx += goroutines {
				key := genKey(idx)
				fileID := uint64(idx + 1)
				if err := tree.Insert(key, fileID); err != nil {
					errCh <- fmt.Errorf("goroutine %d: Insert(%q): %w", g, key, err)
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

	inserted := make(map[string]uint64, n)
	for idx := 0; idx < n; idx++ {
		inserted[genKey(idx)] = uint64(idx + 1)
	}

	finalRoot := tree.Root()
	assertAllLookupable(t, store, finalRoot, inserted)
	assertStructuralInvariants(t, store, finalRoot, n)
}

// TestCrabbingConcurrentPropagateNoDeadlock is a permanent, fast (well under
// a second), deterministic regression test for GitHub issue #9's confirmed
// deadlock finding (2a.4.2 fix round 3): a large-scale concurrent stress run
// (160 goroutines / 80,000 keys) was directly observed genuinely deadlocked
// for 40+ minutes, with multiple goroutines permanently blocked on
// sync.Mutex.Lock inside NodeStore.Lock, called from crabInsert/findParent,
// each waiting on a node latch held by another blocked goroutine -- a
// lock-ordering cycle, not just a data race.
//
// The fix makes every hand-over-hand ("lock next, release current") step in
// crabInsert/findParent use TryLock instead of a blocking Lock, unlocking
// everything held and restarting the whole walk from the root the instant a
// TryLock would otherwise have blocked (see errRestartFromRoot in
// insert.go). This test uses a synchronization hook (crabRetryHook), in the
// same spirit as engine/mvcc's hook-based pause pattern (see
// newSnapshotWithHook/commitVersionWithHook), to FORCE that exact
// interleaving deterministically -- one goroutine holding a node's latch
// while another's crabInsert tries to acquire the very same latch as its
// next hand-over-hand step -- instead of relying on massive random
// concurrency to hit it by chance, so this test can iterate in well under a
// second instead of tens of minutes.
func TestCrabbingConcurrentPropagateNoDeadlock(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const prebuilt = 250 // forces at least one leaf split into an internal root (see testInsertLeafSplit)
	rootID, inserted := insertN(t, store, alloc, prebuilt)
	tree := NewTree(store, alloc, rootID)

	isLeaf, _, rootInternal, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf || len(rootInternal.Children) < 2 {
		t.Fatalf("prebuilt tree root is not an internal node with >= 2 children (isLeaf=%v children=%v); test setup assumption broken", isLeaf, rootInternal.Children)
	}

	// Mirror crabInsert's own routing logic exactly so contendedChildID is
	// guaranteed to be the node the Insert below will actually try to
	// TryLock as its very next hand-over-hand step after the (uncontended)
	// root lock.
	newKey := genKey(prebuilt)
	childIdx := sort.Search(len(rootInternal.Keys), func(i int) bool { return newKey < rootInternal.Keys[i] })
	contendedChildID := rootInternal.Children[childIdx]

	// Simulate another goroutine that is already holding contendedChildID's
	// latch (e.g. mid-crabInsert itself, holding it as its own "current"
	// node) by locking it directly here, before starting the Insert we
	// actually want to observe.
	store.Lock(contendedChildID)

	var restarts int32
	prevHook := crabRetryHook
	crabRetryHook = func(nodeID uint64) {
		if nodeID == contendedChildID {
			atomic.AddInt32(&restarts, 1)
		}
	}
	t.Cleanup(func() { crabRetryHook = prevHook })

	release := make(chan struct{})
	go func() {
		<-release
		store.Unlock(contendedChildID)
	}()

	done := make(chan error, 1)
	go func() { done <- tree.Insert(newKey, uint64(prebuilt+1)) }()

	// Wait for the Insert goroutine to actually hit the contended latch and
	// register at least one restart-from-root before releasing it. Bounded
	// so this test can never hang even if the fix regresses to a blocking
	// Lock here -- that would instead surface as a clear t.Fatal on this 2s
	// timeout, never as a silent multi-hour hang.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&restarts) == 0 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("Insert never hit the contended latch on node %d within 2s; test setup did not exercise the intended interleaving", contendedChildID)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Insert: unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Insert did not complete within 5s after the contended latch was released: deadlock regression (GitHub issue #9)")
	}

	if atomic.LoadInt32(&restarts) == 0 {
		t.Fatal("crabRetryHook was never invoked for the contended node; test did not actually exercise the restart-from-root path")
	}

	inserted[newKey] = uint64(prebuilt + 1)
	finalRoot := tree.Root()
	assertAllLookupable(t, store, finalRoot, inserted)
	assertStructuralInvariants(t, store, finalRoot, len(inserted))
}

// TestCrabbingRetryCapSurfacesError is subtask 4.5.1.2's test spec: force
// TryLock to always fail against a target node -- by permanently holding its
// latch from a separate goroutine for the entire test, the same real-lock-
// contention technique TestCrabbingConcurrentPropagateNoDeadlock uses to
// force a single restart, just never released -- so crabInsert/crabDelete
// can never make progress and are forced to exhaust crabMaxRestarts on every
// single attempt. Asserts the call returns errTooManyRestarts within a
// generous but bounded wall-clock timeout, instead of hanging forever: a
// regression back to an unbounded retry loop would surface here as a clear
// t.Fatal on that timeout, never as a silent multi-minute (or worse) hang.
func TestCrabbingRetryCapSurfacesError(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const prebuilt = 250 // forces at least one leaf split into an internal root (see testInsertLeafSplit)
	rootID, inserted := insertN(t, store, alloc, prebuilt)
	tree := NewTree(store, alloc, rootID)

	isLeaf, _, rootInternal, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf || len(rootInternal.Children) < 2 {
		t.Fatalf("prebuilt tree root is not an internal node with >= 2 children (isLeaf=%v children=%v); test setup assumption broken", isLeaf, rootInternal.Children)
	}

	// Mirror crabInsert's own routing logic exactly, as
	// TestCrabbingConcurrentPropagateNoDeadlock does, so contendedChildID is
	// guaranteed to be the node every attempt's next hand-over-hand step will
	// target -- both for the new key inserted below and for the existing key
	// deleted below, since both route through the same child at this level
	// for a key >= every key already in the prebuilt tree.
	newKey := genKey(prebuilt)
	childIdx := sort.Search(len(rootInternal.Keys), func(i int) bool { return newKey < rootInternal.Keys[i] })
	contendedChildID := rootInternal.Children[childIdx]

	// Hold contendedChildID's latch for the rest of the test: every
	// hand-over-hand TryLock attempt against it, on every single restart
	// attempt, will miss. crabInsert/crabDelete therefore have no way to ever
	// make progress and must hit crabMaxRestarts.
	store.Lock(contendedChildID)
	t.Cleanup(func() { store.Unlock(contendedChildID) })

	var restarts int32
	prevHook := crabRetryHook
	crabRetryHook = func(nodeID uint64) {
		if nodeID == contendedChildID {
			atomic.AddInt32(&restarts, 1)
		}
	}
	t.Cleanup(func() { crabRetryHook = prevHook })

	// crabMaxRestarts (1000) attempts at crabRetryBackoff's capped 2ms
	// ceiling is at most ~2s; 30s is a generous, hang-safe upper bound for
	// this bounded-but-slow operation, not a tight timing assertion.
	const timeout = 30 * time.Second

	t.Run("Insert", func(t *testing.T) {
		done := make(chan error, 1)
		go func() { done <- tree.Insert(newKey, uint64(prebuilt+1)) }()

		select {
		case err := <-done:
			if err != errTooManyRestarts {
				t.Fatalf("Insert: got err=%v, want errTooManyRestarts", err)
			}
		case <-time.After(timeout):
			t.Fatalf("Insert did not return within %s: crabMaxRestarts regression (unbounded retry loop hung instead of surfacing errTooManyRestarts)", timeout)
		}
		if atomic.LoadInt32(&restarts) == 0 {
			t.Fatal("crabRetryHook was never invoked for the contended node; test did not actually exercise the restart-from-root path")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		atomic.StoreInt32(&restarts, 0)

		// Delete the largest already-inserted key (genKey(prebuilt-1)): being
		// the second-largest key overall (just below newKey), it routes
		// through the exact same root-level child boundary as newKey did
		// above -- i.e. contendedChildID -- so this exercises the identical
		// permanently-contended hand-over-hand step crabDeleteOnce takes.
		deleteKey := genKey(prebuilt - 1)
		if _, ok := inserted[deleteKey]; !ok {
			t.Fatalf("test setup assumption broken: %q was not actually inserted", deleteKey)
		}

		done := make(chan error, 1)
		go func() {
			_, err := tree.Delete(deleteKey)
			done <- err
		}()

		select {
		case err := <-done:
			if err != errTooManyRestarts {
				t.Fatalf("Delete: got err=%v, want errTooManyRestarts", err)
			}
		case <-time.After(timeout):
			t.Fatalf("Delete did not return within %s: crabMaxRestarts regression (unbounded retry loop hung instead of surfacing errTooManyRestarts)", timeout)
		}
	})
}
