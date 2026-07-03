package btree

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"
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
func assertStructuralInvariants(t *testing.T, store *NodeStore, rootID uint64, wantKeyCount int) {
	t.Helper()

	// Recursively validate every internal node's invariants (sorted keys,
	// correct fanout: len(Children) == len(Keys)+1).
	var validate func(nodeID uint64)
	validate = func(nodeID uint64) {
		isLeaf, _, internal, err := store.ReadNode(nodeID)
		if err != nil {
			t.Fatalf("ReadNode(%d): unexpected error: %v", nodeID, err)
		}
		if isLeaf {
			return
		}
		if len(internal.Children) != len(internal.Keys)+1 {
			t.Fatalf("internal node %d: len(Children)=%d, want len(Keys)+1=%d", nodeID, len(internal.Children), len(internal.Keys)+1)
		}
		if !sort.StringsAreSorted(internal.Keys) {
			t.Fatalf("internal node %d: Keys not sorted ascending: %v", nodeID, internal.Keys)
		}
		for _, child := range internal.Children {
			validate(child)
		}
	}
	validate(rootID)

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
