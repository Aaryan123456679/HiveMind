package btree

import (
	"fmt"
	"os"
	"sort"
)

// NodeStore is the minimal file-I/O layer this subtask (1.2.2) adds on top of
// subtask 1.2.1's pure in-memory Encode/Decode: it addresses on-disk nodes by a
// uint64 node ID and reads/writes exactly NodeSize bytes at a time. It wraps the
// *os.File returned by OpenIndexFile.
//
// Addressing convention: node ID N lives at byte offset N * NodeSize within the
// index file, directly analogous to engine/catalog/file.go's FileManager mapping
// pageID -> pageID * PageSize. Node ID 0 is reserved and never a valid node
// (mirroring node.go's noSibling sentinel and catalog's reserved free-list page 0);
// real node IDs start at 1.
//
// NodeStore deliberately does NOT implement an allocator or free-list (unlike
// catalog's FileManager): this subtask only needs to read nodes that were placed at
// caller-chosen IDs (by the test-scaffolding tree-builder in lookup_test.go today,
// and by the real insert-with-splitting implementation in a later subtask). If a
// free-list/allocator turns out to be needed, that is left to whichever later
// subtask's LLD calls for it.
//
// NodeStore also does not do any locking: concurrency (latch-crabbing writes,
// optimistic lock-free reads with version-counter retry) is explicitly deferred to
// a later, concurrency-focused subtask per docs/LLD/btree.md's "Concurrency"
// section. This subtask is single-threaded.
type NodeStore struct {
	f *os.File
}

// NewNodeStore wraps an already-open index file handle (as returned by
// OpenIndexFile) in a NodeStore.
func NewNodeStore(f *os.File) *NodeStore {
	return &NodeStore{f: f}
}

// reservedNodeID is the sentinel node ID that is never a valid node (mirrors
// noSibling / catalog's reserved page 0). Real node IDs start at 1.
const reservedNodeID uint64 = 0

// ReadNode reads and decodes the node at nodeID. Exactly one of leaf/internal is
// populated, indicated by isLeaf. It returns an error for I/O failures, a short
// read, or a corrupt/undecodable node -- never for anything related to key
// presence (that is Lookup's concern, not ReadNode's).
func (s *NodeStore) ReadNode(nodeID uint64) (isLeaf bool, leaf LeafNode, internal InternalNode, err error) {
	if nodeID == reservedNodeID {
		return false, LeafNode{}, InternalNode{}, fmt.Errorf("btree: node ID %d is reserved and never valid", nodeID)
	}

	buf := make([]byte, NodeSize)
	offset := int64(nodeID) * int64(NodeSize)
	if _, err := s.f.ReadAt(buf, offset); err != nil {
		return false, LeafNode{}, InternalNode{}, fmt.Errorf("btree: reading node %d at offset %d: %w", nodeID, offset, err)
	}

	switch buf[offNodeType] {
	case nodeTypeLeaf:
		l, err := DecodeLeafNode(buf)
		if err != nil {
			return false, LeafNode{}, InternalNode{}, fmt.Errorf("btree: decoding leaf node %d: %w", nodeID, err)
		}
		return true, l, InternalNode{}, nil
	case nodeTypeInternal:
		n, err := DecodeInternalNode(buf)
		if err != nil {
			return false, LeafNode{}, InternalNode{}, fmt.Errorf("btree: decoding internal node %d: %w", nodeID, err)
		}
		return false, LeafNode{}, n, nil
	default:
		return false, LeafNode{}, InternalNode{}, fmt.Errorf("btree: node %d has unrecognized type discriminator %d", nodeID, buf[offNodeType])
	}
}

// WriteNode writes an already-encoded (via LeafNode.Encode / InternalNode.Encode)
// node buffer at nodeID. encoded must be exactly NodeSize bytes, which Encode
// always produces.
func (s *NodeStore) WriteNode(nodeID uint64, encoded []byte) error {
	if nodeID == reservedNodeID {
		return fmt.Errorf("btree: node ID %d is reserved and never valid", nodeID)
	}
	if len(encoded) != NodeSize {
		return fmt.Errorf("btree: WriteNode requires exactly %d bytes, got %d", NodeSize, len(encoded))
	}

	offset := int64(nodeID) * int64(NodeSize)
	if _, err := s.f.WriteAt(encoded, offset); err != nil {
		return fmt.Errorf("btree: writing node %d at offset %d: %w", nodeID, offset, err)
	}
	return nil
}

// Lookup performs a point lookup of path in the B+Tree rooted at rootNodeID,
// reading nodes from store. It returns the fileID and found=true if path is
// present; found=false and a nil error if path is genuinely absent (not-found is a
// normal, expected outcome, not an error). A non-nil error indicates a genuine
// I/O or decode failure encountered while traversing the tree.
//
// Traversal follows the covering convention documented on InternalNode: for an
// internal node with keys K[0..n) and children C[0..n], C[i] covers keys in
// [K[i-1], K[i]) for interior i (K[-1] treated as -infinity, K[n] treated as
// +infinity). This is implemented as "descend into the child before the first key
// strictly greater than path".
func Lookup(store *NodeStore, rootNodeID uint64, path string) (fileID uint64, found bool, err error) {
	currentID := rootNodeID
	for {
		isLeaf, leaf, internal, err := store.ReadNode(currentID)
		if err != nil {
			return 0, false, err
		}

		if isLeaf {
			i := sort.SearchStrings(leaf.Keys, path)
			if i < len(leaf.Keys) && leaf.Keys[i] == path {
				return leaf.FileIDs[i], true, nil
			}
			return 0, false, nil
		}

		// Internal node: find the first key strictly greater than path, and
		// descend into the child immediately before it.
		i := sort.Search(len(internal.Keys), func(i int) bool { return path < internal.Keys[i] })
		currentID = internal.Children[i]
	}
}
