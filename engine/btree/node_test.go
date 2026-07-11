package btree

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestNodeSerialization exercises the full test spec for subtask 1.2.1: encode/decode
// leaf and internal nodes with varying key counts, assert equality; also verifies
// overflow is rejected (not truncated), and that OpenIndexFile creates the index file on
// first use.
func TestNodeSerialization(t *testing.T) {
	t.Run("leaf round-trip", func(t *testing.T) {
		cases := []LeafNode{
			// zero keys
			{Keys: nil, FileIDs: nil, NextLeaf: 0},
			// one key
			{Keys: []string{"auth/login"}, FileIDs: []uint64{42}, NextLeaf: 7},
			// many keys, rightmost leaf (no sibling), non-zero version counter
			{
				Keys:     []string{"auth/login", "auth/logout", "auth/oauth", "billing/invoice", "billing/refund"},
				FileIDs:  []uint64{1, 2, 3, 4, 5},
				NextLeaf: 0,
				Version:  99,
			},
		}

		for i, want := range cases {
			encoded, err := want.Encode()
			if err != nil {
				t.Fatalf("case %d: Encode() error = %v, want nil", i, err)
			}
			if len(encoded) != NodeSize {
				t.Fatalf("case %d: Encode() returned %d bytes, want %d", i, len(encoded), NodeSize)
			}

			got, err := DecodeLeafNode(encoded)
			if err != nil {
				t.Fatalf("case %d: DecodeLeafNode() error = %v, want nil", i, err)
			}

			if !reflect.DeepEqual(normalizeLeaf(want), normalizeLeaf(got)) {
				t.Fatalf("case %d: round-trip mismatch:\n  want %+v\n  got  %+v", i, want, got)
			}
		}
	})

	t.Run("internal round-trip", func(t *testing.T) {
		cases := []InternalNode{
			// degenerate empty-root case: 0 keys, 1 child
			{Keys: nil, Children: []uint64{1}},
			// one key, two children
			{Keys: []string{"billing/invoice"}, Children: []uint64{1, 2}},
			// many keys, non-zero version counter
			{
				Keys:     []string{"auth/oauth", "billing/invoice", "billing/refund", "support/ticket"},
				Children: []uint64{1, 2, 3, 4, 5},
				Version:  7,
			},
		}

		for i, want := range cases {
			encoded, err := want.Encode()
			if err != nil {
				t.Fatalf("case %d: Encode() error = %v, want nil", i, err)
			}
			if len(encoded) != NodeSize {
				t.Fatalf("case %d: Encode() returned %d bytes, want %d", i, len(encoded), NodeSize)
			}

			got, err := DecodeInternalNode(encoded)
			if err != nil {
				t.Fatalf("case %d: DecodeInternalNode() error = %v, want nil", i, err)
			}

			if !reflect.DeepEqual(normalizeInternal(want), normalizeInternal(got)) {
				t.Fatalf("case %d: round-trip mismatch:\n  want %+v\n  got  %+v", i, want, got)
			}
		}
	})

	t.Run("overflow rejected", func(t *testing.T) {
		// Build a leaf whose keys alone exceed NodeSize, and assert Encode returns an
		// error rather than a truncated buffer.
		bigKey := strings.Repeat("x", NodeSize) // one key alone already exceeds NodeSize
		leaf := LeafNode{Keys: []string{bigKey}, FileIDs: []uint64{1}, NextLeaf: 0}
		if _, err := leaf.Encode(); err == nil {
			t.Fatalf("LeafNode.Encode() with oversized key = nil error, want non-nil error")
		}

		internal := InternalNode{Keys: []string{bigKey}, Children: []uint64{1, 2}}
		if _, err := internal.Encode(); err == nil {
			t.Fatalf("InternalNode.Encode() with oversized key = nil error, want non-nil error")
		}

		// Also verify many small keys can overflow via cumulative size, not just a
		// single huge key.
		manyKeys := make([]string, 0, 1000)
		manyIDs := make([]uint64, 0, 1000)
		for i := 0; i < 1000; i++ {
			manyKeys = append(manyKeys, strings.Repeat("k", 20))
			manyIDs = append(manyIDs, uint64(i))
		}
		bigLeaf := LeafNode{Keys: manyKeys, FileIDs: manyIDs, NextLeaf: 0}
		if _, err := bigLeaf.Encode(); err == nil {
			t.Fatalf("LeafNode.Encode() with cumulative oversized keys = nil error, want non-nil error")
		}

		// Mismatched FileIDs/Keys length must also error.
		mismatched := LeafNode{Keys: []string{"a", "b"}, FileIDs: []uint64{1}}
		if _, err := mismatched.Encode(); err == nil {
			t.Fatalf("LeafNode.Encode() with mismatched FileIDs/Keys length = nil error, want non-nil error")
		}

		mismatchedInternal := InternalNode{Keys: []string{"a"}, Children: []uint64{1, 2, 3}}
		if _, err := mismatchedInternal.Encode(); err == nil {
			t.Fatalf("InternalNode.Encode() with mismatched Children/Keys length = nil error, want non-nil error")
		}
	})

	t.Run("decode rejects mismatched node type", func(t *testing.T) {
		leaf := LeafNode{Keys: []string{"auth/login"}, FileIDs: []uint64{1}, NextLeaf: 0}
		encodedLeaf, err := leaf.Encode()
		if err != nil {
			t.Fatalf("LeafNode.Encode() error = %v, want nil", err)
		}
		if _, err := DecodeInternalNode(encodedLeaf); err == nil {
			t.Fatalf("DecodeInternalNode() on a leaf-tagged buffer = nil error, want non-nil error")
		}

		internal := InternalNode{Keys: []string{"auth/login"}, Children: []uint64{1, 2}}
		encodedInternal, err := internal.Encode()
		if err != nil {
			t.Fatalf("InternalNode.Encode() error = %v, want nil", err)
		}
		if _, err := DecodeLeafNode(encodedInternal); err == nil {
			t.Fatalf("DecodeLeafNode() on an internal-tagged buffer = nil error, want non-nil error")
		}
	})

	t.Run("file created on first use", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "index", "name.idx")

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) failed: %v", filepath.Dir(path), err)
		}

		if _, err := os.Stat(path); err == nil {
			t.Fatalf("index file %q already exists before OpenIndexFile", path)
		}

		f, err := OpenIndexFile(path)
		if err != nil {
			t.Fatalf("OpenIndexFile(%q) error = %v, want nil", path, err)
		}
		defer f.Close()

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat(%q) failed after OpenIndexFile: %v", path, err)
		}
		if info.IsDir() {
			t.Fatalf("index file %q is a directory, want a regular file", path)
		}

		// Reopening the same path must not error or wipe existing content.
		if _, err := f.WriteString("hello"); err != nil {
			t.Fatalf("WriteString to index file failed: %v", err)
		}
		f.Close()

		f2, err := OpenIndexFile(path)
		if err != nil {
			t.Fatalf("second OpenIndexFile(%q) error = %v, want nil", path, err)
		}
		defer f2.Close()

		info2, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat(%q) failed after second OpenIndexFile: %v", path, err)
		}
		if info2.Size() == 0 {
			t.Fatalf("index file %q was truncated by second OpenIndexFile call", path)
		}
	})
}

// normalizeLeaf converts nil slices to empty slices so reflect.DeepEqual treats a
// zero-key node encoded-then-decoded (which necessarily produces empty, non-nil slices
// on the decode side) as equal to a caller-constructed node using nil slices.
func normalizeLeaf(n LeafNode) LeafNode {
	if n.Keys == nil {
		n.Keys = []string{}
	}
	if n.FileIDs == nil {
		n.FileIDs = []uint64{}
	}
	return n
}

func normalizeInternal(n InternalNode) InternalNode {
	if n.Keys == nil {
		n.Keys = []string{}
	}
	if n.Children == nil {
		n.Children = []uint64{}
	}
	return n
}

// newLatchTestStore opens a fresh, isolated (t.TempDir()) index file and wraps it in
// a NodeStore, ready for direct WriteNode/Lock/Unlock/Version calls (the level this
// subtask's test spec is scoped to -- it deliberately does not go through
// Insert/NodeAllocator, since 2a.4.1 is about the node-latch/version fields
// themselves, not the higher-level tree algorithms built on top of them later).
func newLatchTestStore(t *testing.T) *NodeStore {
	t.Helper()

	path := filepath.Join(t.TempDir(), "name.idx")
	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	return NewNodeStore(f)
}

// encodeTestLeaf is a small helper producing a validly-encoded leaf buffer for the
// given key/fileID pair, for use as WriteNode's payload in latch tests below (the
// actual leaf contents are incidental to these tests; only the version-counter
// behavior matters).
func encodeTestLeaf(t *testing.T, key string, fileID uint64) []byte {
	t.Helper()
	buf, err := LeafNode{Keys: []string{key}, FileIDs: []uint64{fileID}}.Encode()
	if err != nil {
		t.Fatalf("LeafNode.Encode: %v", err)
	}
	return buf
}

// TestEncodeKeysNodeSizeInvariant covers subtask 4.5.12.2's acceptance criterion:
// encodeKeys' uint16 key-length prefix is only overflow-safe as long as NodeSize stays
// under 65536, so this asserts that coupling holds at runtime (in addition to the
// nodeSizeFitsUint16LengthPrefix compile-time assertion in node.go, which would instead
// fail `go build`/`go vet` outright if NodeSize were ever raised to >= 65536). This is a
// guard-existence check, not a behavioral test: no encode/decode outcome changes here.
func TestEncodeKeysNodeSizeInvariant(t *testing.T) {
	if NodeSize >= 65536 {
		t.Fatalf("NodeSize invariant violated: NodeSize=%d must stay under 65536, or "+
			"encodeKeys' uint16 key-length prefix can silently wrap/truncate a long "+
			"enough key instead of being rejected at encode time", NodeSize)
	}

	// nodeSizeFitsUint16LengthPrefix (node.go) is itself a compile-time assertion of this
	// same invariant; referencing it here just confirms it is still in scope and wired
	// to NodeSize, not a disconnected/dead constant.
	if nodeSizeFitsUint16LengthPrefix != uint16(65535-NodeSize) {
		t.Fatalf("nodeSizeFitsUint16LengthPrefix = %d, want %d (65535 - NodeSize)",
			nodeSizeFitsUint16LengthPrefix, uint16(65535-NodeSize))
	}
}

// TestNodeLatchFields covers subtask 2a.4.1's acceptance criterion: every node
// carries a latch (NodeStore.Lock/Unlock, keyed by node ID -- see latch.go) and a
// version counter (NodeStore.Version) that increments on any structural mutation to
// that node (bumped by exactly one inside WriteNode, per its doc comment).
func TestNodeLatchFields(t *testing.T) {
	t.Run("version starts at zero for an unwritten node", func(t *testing.T) {
		store := newLatchTestStore(t)
		if got := store.Version(1); got != 0 {
			t.Fatalf("Version(1) on a never-written node = %d, want 0", got)
		}
	})

	t.Run("single mutation increments version exactly once", func(t *testing.T) {
		store := newLatchTestStore(t)
		const nodeID = 1

		before := store.Version(nodeID)
		store.Lock(nodeID)
		if err := store.WriteNode(nodeID, encodeTestLeaf(t, "auth/login", 1)); err != nil {
			store.Unlock(nodeID)
			t.Fatalf("WriteNode: %v", err)
		}
		store.Unlock(nodeID)
		after := store.Version(nodeID)

		if after != before+1 {
			t.Fatalf("version after one mutation = %d, want %d (before=%d)", after, before+1, before)
		}
	})

	t.Run("multiple sequential mutations increment monotonically, once each", func(t *testing.T) {
		store := newLatchTestStore(t)
		const nodeID = 1
		const mutations = 5

		for i := 0; i < mutations; i++ {
			before := store.Version(nodeID)
			store.Lock(nodeID)
			if err := store.WriteNode(nodeID, encodeTestLeaf(t, "auth/login", uint64(i))); err != nil {
				store.Unlock(nodeID)
				t.Fatalf("WriteNode (mutation %d): %v", i, err)
			}
			store.Unlock(nodeID)
			after := store.Version(nodeID)
			if after != before+1 {
				t.Fatalf("mutation %d: version = %d, want %d (before=%d)", i, after, before+1, before)
			}
		}

		if got := store.Version(nodeID); got != uint64(mutations) {
			t.Fatalf("final version = %d, want %d", got, mutations)
		}
	})

	t.Run("mutating one node does not affect another node's version", func(t *testing.T) {
		store := newLatchTestStore(t)

		store.Lock(1)
		if err := store.WriteNode(1, encodeTestLeaf(t, "auth/login", 1)); err != nil {
			store.Unlock(1)
			t.Fatalf("WriteNode(1): %v", err)
		}
		store.Unlock(1)

		if got := store.Version(1); got != 1 {
			t.Fatalf("Version(1) = %d, want 1", got)
		}
		if got := store.Version(2); got != 0 {
			t.Fatalf("Version(2) = %d, want 0 (untouched node must not be affected)", got)
		}
	})

	t.Run("concurrent mutations under real locking increment version exactly once per mutation, -race clean", func(t *testing.T) {
		store := newLatchTestStore(t)
		const nodeID = 1
		const goroutines = 50
		const mutationsPerGoroutine = 20
		const totalMutations = goroutines * mutationsPerGoroutine

		var wg sync.WaitGroup
		wg.Add(goroutines)
		for g := 0; g < goroutines; g++ {
			go func(g int) {
				defer wg.Done()
				for i := 0; i < mutationsPerGoroutine; i++ {
					store.Lock(nodeID)
					if err := store.WriteNode(nodeID, encodeTestLeaf(t, "auth/login", uint64(g*mutationsPerGoroutine+i))); err != nil {
						store.Unlock(nodeID)
						t.Errorf("WriteNode: %v", err)
						return
					}
					store.Unlock(nodeID)
				}
			}(g)
		}
		wg.Wait()

		if got := store.Version(nodeID); got != uint64(totalMutations) {
			t.Fatalf("final version = %d, want %d (every one of %d concurrent mutations must increment exactly once, no lost updates, no double counts)", got, totalMutations, totalMutations)
		}
	})
}
