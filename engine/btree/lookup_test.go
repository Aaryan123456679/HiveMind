package btree

import (
	"path/filepath"
	"testing"
)

// buildTestTree is TEST-ONLY SCAFFOLDING for exercising Lookup. It is NOT subtask
// 1.2.3's real insert-with-splitting API: it hand-constructs a fixed, already-
// balanced tree shape by directly assembling LeafNode/InternalNode values and
// writing them to disk via NodeStore. It performs no splitting, no rebalancing, and
// no general-purpose insert logic -- it exists solely because 1.2.3 (real insert)
// has not landed yet and Lookup needs *some* on-disk tree to traverse. Do not reuse
// this as, or mistake it for, the real insert implementation.
//
// Tree shape (3 levels):
//
//	root (internal, node ID 7): Keys=["billing/invoice"], Children=[internal1, internal2]
//	  internal1 (node ID 5): Keys=["auth/oauth"],     Children=[leaf1, leaf2]
//	    leaf1 (node ID 1): "auth/login"=101, "auth/logout"=102        -> NextLeaf leaf2
//	    leaf2 (node ID 2): "auth/oauth"=103, "auth/session"=104       -> NextLeaf leaf3
//	  internal2 (node ID 6): Keys=["search/index"],    Children=[leaf3, leaf4]
//	    leaf3 (node ID 3): "billing/invoice"=201, "billing/plan"=202  -> NextLeaf leaf4
//	    leaf4 (node ID 4): "search/index"=301, "search/query"=302    -> NextLeaf 0 (none)
//
// Node IDs are assigned arbitrarily (leaves 1-4, internals 5-6, root 7); only that
// every ID is >= 1 (0 is reserved) and consistent with the Children/NextLeaf
// pointers matters.
func buildTestTree(t *testing.T) (store *NodeStore, rootID uint64) {
	t.Helper()

	const (
		leaf1ID = uint64(1)
		leaf2ID = uint64(2)
		leaf3ID = uint64(3)
		leaf4ID = uint64(4)
		int1ID  = uint64(5)
		int2ID  = uint64(6)
		rootID_ = uint64(7)
	)

	path := filepath.Join(t.TempDir(), "name.idx")
	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	store = NewNodeStore(f)

	nodes := []struct {
		id       uint64
		leaf     *LeafNode
		internal *InternalNode
	}{
		{id: leaf1ID, leaf: &LeafNode{
			Keys: []string{"auth/login", "auth/logout"}, FileIDs: []uint64{101, 102}, NextLeaf: leaf2ID,
		}},
		{id: leaf2ID, leaf: &LeafNode{
			Keys: []string{"auth/oauth", "auth/session"}, FileIDs: []uint64{103, 104}, NextLeaf: leaf3ID,
		}},
		{id: leaf3ID, leaf: &LeafNode{
			Keys: []string{"billing/invoice", "billing/plan"}, FileIDs: []uint64{201, 202}, NextLeaf: leaf4ID,
		}},
		{id: leaf4ID, leaf: &LeafNode{
			Keys: []string{"search/index", "search/query"}, FileIDs: []uint64{301, 302}, NextLeaf: noSibling,
		}},
		{id: int1ID, internal: &InternalNode{
			Keys: []string{"auth/oauth"}, Children: []uint64{leaf1ID, leaf2ID},
		}},
		{id: int2ID, internal: &InternalNode{
			Keys: []string{"search/index"}, Children: []uint64{leaf3ID, leaf4ID},
		}},
		{id: rootID_, internal: &InternalNode{
			Keys: []string{"billing/invoice"}, Children: []uint64{int1ID, int2ID},
		}},
	}

	for _, n := range nodes {
		var encoded []byte
		var err error
		if n.leaf != nil {
			encoded, err = n.leaf.Encode()
		} else {
			encoded, err = n.internal.Encode()
		}
		if err != nil {
			t.Fatalf("encoding node %d: %v", n.id, err)
		}
		if err := store.WriteNode(n.id, encoded); err != nil {
			t.Fatalf("writing node %d: %v", n.id, err)
		}
	}

	return store, rootID_
}

func TestLookup(t *testing.T) {
	store, rootID := buildTestTree(t)

	t.Run("present", func(t *testing.T) {
		cases := []struct {
			path       string
			wantFileID uint64
		}{
			{"auth/login", 101},
			{"auth/logout", 102},
			{"auth/oauth", 103},
			{"auth/session", 104},
			{"billing/invoice", 201},
			{"billing/plan", 202},
			{"search/index", 301},
			{"search/query", 302},
		}
		for _, tc := range cases {
			fileID, found, err := Lookup(store, rootID, tc.path)
			if err != nil {
				t.Fatalf("Lookup(%q): unexpected error: %v", tc.path, err)
			}
			if !found {
				t.Fatalf("Lookup(%q): expected found=true, got false", tc.path)
			}
			if fileID != tc.wantFileID {
				t.Fatalf("Lookup(%q): fileID = %d, want %d", tc.path, fileID, tc.wantFileID)
			}
		}
	})

	t.Run("absent", func(t *testing.T) {
		// These paths sort into leaves that DO have other real keys, proving
		// Lookup genuinely checks for an exact key match rather than just
		// treating "found a leaf" as success.
		cases := []string{
			"auth/middleware", // between auth/logout and auth/oauth
			"billing/refund",  // after billing/plan, within billing's range
		}
		for _, path := range cases {
			fileID, found, err := Lookup(store, rootID, path)
			if err != nil {
				t.Fatalf("Lookup(%q): unexpected error: %v", path, err)
			}
			if found {
				t.Fatalf("Lookup(%q): expected found=false, got true (fileID=%d)", path, fileID)
			}
			if fileID != 0 {
				t.Fatalf("Lookup(%q): expected fileID=0 on not-found, got %d", path, fileID)
			}
		}
	})

	t.Run("boundary", func(t *testing.T) {
		cases := []string{
			"aaa/first", // sorts before every key in the whole tree
			"zzz/last",  // sorts after every key in the whole tree
		}
		for _, path := range cases {
			fileID, found, err := Lookup(store, rootID, path)
			if err != nil {
				t.Fatalf("Lookup(%q): unexpected error: %v", path, err)
			}
			if found {
				t.Fatalf("Lookup(%q): expected found=false, got true (fileID=%d)", path, fileID)
			}
			if fileID != 0 {
				t.Fatalf("Lookup(%q): expected fileID=0 on not-found, got %d", path, fileID)
			}
		}
	})
}
