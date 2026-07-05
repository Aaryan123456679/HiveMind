package btree

import (
	"fmt"
	"os"
	"sort"
	"sync"
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
// NodeStore also owns the in-memory per-node-ID latch/version registry described in
// latch.go: concurrency (latch-crabbing writes, optimistic lock-free reads with
// version-counter retry) is wired up field-by-field starting with this subtask
// (2a.4.1) and built on by 2a.4.2-2a.4.5, per docs/LLD/btree.md's "Concurrency"
// section. Node content itself is still read/written as fresh value structs on every
// call (no in-memory node cache); latches/versions are keyed by node ID rather than
// attached to a cached node object.
type NodeStore struct {
	f *os.File

	// latchesMu guards latches. latches is lazily populated: see latchFor in
	// latch.go.
	latchesMu sync.Mutex
	latches   map[uint64]*nodeLatch
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
//
// WriteNode is the sole choke point every structural mutation to a node's on-disk
// content flows through (both insert.go and delete.go funnel all node writes through
// it), so it is where nodeID's version counter is bumped: on a successful write,
// WriteNode increments nodeID's version by exactly one (see nodeLatch's doc comment
// in latch.go for the full protocol this implements and why).
//
// WriteNode deliberately does NOT itself acquire nodeID's latch (Lock/Unlock in
// latch.go): 2a.4.2/2a.4.3's latch-crabbing algorithms hold a node's latch across a
// read-decide-write sequence that calls WriteNode, and re-locking inside WriteNode
// would deadlock against a non-reentrant sync.Mutex. The required convention for any
// concurrent caller is: call Lock(nodeID) before, and Unlock(nodeID) after, the
// WriteNode call(s) that mutate nodeID. This subtask's existing single-threaded call
// sites (insert.go, delete.go) do not yet do this; wiring that discipline into them
// is 2a.4.2/2a.4.3's job.
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

	s.latchFor(nodeID).version.Add(1)
	return nil
}

// descendToLeaf walks the B+Tree rooted at rootNodeID from root to the leaf
// that would contain path, reading nodes from store. It returns the ordered
// chain of node IDs visited (chain[0] == rootNodeID, chain[len(chain)-1] ==
// the leaf's node ID) plus the decoded leaf itself, so callers don't need to
// re-read the leaf a second time. This is the single place descent logic
// lives; both Lookup and Insert (engine/btree/insert.go) call this instead of
// each re-implementing the traversal.
//
// Traversal follows the covering convention documented on InternalNode: for an
// internal node with keys K[0..n) and children C[0..n], C[i] covers keys in
// [K[i-1], K[i]) for interior i (K[-1] treated as -infinity, K[n] treated as
// +infinity). This is implemented as "descend into the child before the first
// key strictly greater than path".
func descendToLeaf(store *NodeStore, rootNodeID uint64, path string) (chain []uint64, leaf LeafNode, err error) {
	currentID := rootNodeID
	chain = append(chain, currentID)
	for {
		isLeaf, l, internal, err := store.ReadNode(currentID)
		if err != nil {
			return nil, LeafNode{}, err
		}

		if isLeaf {
			return chain, l, nil
		}

		// Internal node: find the first key strictly greater than path, and
		// descend into the child immediately before it.
		i := sort.Search(len(internal.Keys), func(i int) bool { return path < internal.Keys[i] })
		currentID = internal.Children[i]
		chain = append(chain, currentID)
	}
}

// Lookup performs a point lookup of path in the B+Tree rooted at rootNodeID,
// reading nodes from store. It returns the fileID and found=true if path is
// present; found=false and a nil error if path is genuinely absent (not-found is a
// normal, expected outcome, not an error). A non-nil error indicates a genuine
// I/O or decode failure encountered while traversing the tree.
func Lookup(store *NodeStore, rootNodeID uint64, path string) (fileID uint64, found bool, err error) {
	_, leaf, err := descendToLeaf(store, rootNodeID, path)
	if err != nil {
		return 0, false, err
	}

	i := sort.SearchStrings(leaf.Keys, path)
	if i < len(leaf.Keys) && leaf.Keys[i] == path {
		return leaf.FileIDs[i], true, nil
	}
	return 0, false, nil
}
