package btree

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
