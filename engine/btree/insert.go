package btree

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"sync"
)

// nodeAllocSuffix names the small sidecar file, kept alongside the main index
// file, that durably persists the node-ID allocator's high-water-mark. This
// mirrors engine/catalog/idalloc.go's IDAllocator pattern exactly (see
// docs/LLD/btree.md and 1.2.2's NodeStore doc comment, which explicitly notes
// NodeStore itself deliberately does not implement an allocator and leaves
// that decision to whichever subtask first needs one -- this is that
// subtask).
const nodeAllocSuffix = ".nodealloc"

// nodeAllocStateSize is the fixed size, in bytes, of the sidecar file's
// contents: a single little-endian uint64 high-water-mark (the highest node
// ID ever allocated, or 0 if none have been allocated yet).
const nodeAllocStateSize = 8

// NodeAllocator hands out monotonically increasing node IDs for newly
// allocated B+Tree nodes (new leaves created by splits, new internal nodes
// created by splits or by root promotion). It never reuses an ID. IDs start
// at 1 because node ID 0 (reservedNodeID, see lookup.go) is reserved and
// never valid, mirroring node.go's noSibling sentinel and
// engine/catalog/idalloc.go's InvalidFileID convention.
//
// Known gap (documented, expected to be revisited by 1.2.5/1.2.6): this
// allocator durably persists its own high-water-mark across reopen of the
// same index file, but nothing yet persists "the current root node ID" --
// callers of Insert are responsible for tracking the returned newRootNodeID
// themselves for now. Persisting the root pointer is left to the
// persist/reload subtask.
type NodeAllocator struct {
	mu sync.Mutex

	// next is the highest node ID allocated so far (0 if none have been
	// allocated yet for a fresh index file). The next call to Next() will
	// hand out next+1.
	next uint64

	// stateFile is the open sidecar file backing durable persistence of next.
	stateFile *os.File
}

// NewNodeAllocator opens (creating if necessary) a sidecar state file
// alongside store's underlying index file, and restores the in-memory
// high-water-mark from whatever was last durably persisted there (0 for a
// brand-new index file, so the first Next() returns 1).
func NewNodeAllocator(store *NodeStore) (*NodeAllocator, error) {
	path := store.f.Name() + nodeAllocSuffix

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("btree: nodealloc: open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("btree: nodealloc: stat %s: %w", path, err)
	}

	var next uint64
	switch info.Size() {
	case 0:
		// Freshly created sidecar file: no node ID has ever been allocated
		// for this index file. next stays 0, so the first Next() returns 1.
	case nodeAllocStateSize:
		var buf [nodeAllocStateSize]byte
		if _, err := f.ReadAt(buf[:], 0); err != nil {
			f.Close()
			return nil, fmt.Errorf("btree: nodealloc: reading state from %s: %w", path, err)
		}
		next = binary.LittleEndian.Uint64(buf[:])
	default:
		f.Close()
		return nil, fmt.Errorf("btree: nodealloc: state file %s has unexpected size %d (want 0 or %d)", path, info.Size(), nodeAllocStateSize)
	}

	return &NodeAllocator{next: next, stateFile: f}, nil
}

// Next durably allocates and returns the next node ID, starting at 1 (0 is
// reserved, see reservedNodeID). The new high-water-mark is durably persisted
// (WriteAt + Sync) before Next returns successfully, so a subsequent reopen
// of the same index file will never hand out a colliding, already-used node
// ID.
//
// If the durable persist fails, Next returns a non-nil error and does not
// advance the in-memory counter, so the allocator's in-memory state never
// gets ahead of what has actually been made durable.
func (a *NodeAllocator) Next() (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	candidate := a.next + 1

	var buf [nodeAllocStateSize]byte
	binary.LittleEndian.PutUint64(buf[:], candidate)
	if _, err := a.stateFile.WriteAt(buf[:], 0); err != nil {
		return 0, fmt.Errorf("btree: nodealloc: persisting node-ID high-water-mark %d: %w", candidate, err)
	}
	if err := a.stateFile.Sync(); err != nil {
		return 0, fmt.Errorf("btree: nodealloc: syncing node-ID high-water-mark %d: %w", candidate, err)
	}

	a.next = candidate
	return candidate, nil
}

// Close closes the allocator's underlying sidecar file handle.
func (a *NodeAllocator) Close() error {
	return a.stateFile.Close()
}

// Insert inserts path -> fileID into the B+Tree rooted at rootNodeID (or
// bootstraps a brand-new tree if rootNodeID == reservedNodeID, see below),
// splitting nodes that overflow NodeSize as needed, and returns the possibly-
// new root node ID. Callers must use the returned newRootNodeID for all
// subsequent operations (Lookup, further Inserts) against this tree, since a
// split at the root level allocates a brand-new root and the old rootNodeID
// is no longer the tree's root (it does, however, remain a valid, readable
// non-root node).
//
// Empty-tree bootstrap convention (extends 1.2.2's NodeStore/Lookup, which
// did not previously define this case): rootNodeID == reservedNodeID (0)
// means "no root exists yet". Insert allocates a brand-new leaf node
// containing just this one key and returns its ID as the new root. This
// convention is specific to Insert; Lookup is not required to handle
// rootNodeID == reservedNodeID (a Lookup against a tree that has never had
// anything inserted into it is out of scope here).
//
// If path is already present, its fileID is updated in place (upsert
// semantics) and no structural change (and therefore no split) is possible;
// rootNodeID is returned unchanged in that case.
func Insert(store *NodeStore, alloc *NodeAllocator, rootNodeID uint64, path string, fileID uint64) (newRootNodeID uint64, err error) {
	if rootNodeID == reservedNodeID {
		leafID, err := alloc.Next()
		if err != nil {
			return 0, err
		}
		leaf := LeafNode{Keys: []string{path}, FileIDs: []uint64{fileID}, NextLeaf: noSibling}
		if err := writeLeaf(store, leafID, leaf); err != nil {
			return 0, err
		}
		return leafID, nil
	}

	chain, leaf, err := descendToLeaf(store, rootNodeID, path)
	if err != nil {
		return 0, err
	}
	leafID := chain[len(chain)-1]

	i := sort.SearchStrings(leaf.Keys, path)
	if i < len(leaf.Keys) && leaf.Keys[i] == path {
		// Upsert: key already present, just update its fileID. No structural
		// change is possible, so no split can occur and the root never
		// changes.
		leaf.FileIDs[i] = fileID
		if err := writeLeaf(store, leafID, leaf); err != nil {
			return 0, err
		}
		return rootNodeID, nil
	}

	newLeaf := insertIntoLeaf(leaf, i, path, fileID)
	if leafEncodedSize(newLeaf) <= NodeSize {
		if err := writeLeaf(store, leafID, newLeaf); err != nil {
			return 0, err
		}
		return rootNodeID, nil
	}

	// Leaf overflowed: split it. The left half keeps the original leaf's node
	// ID; the right half gets a newly allocated node ID. Per standard
	// B+Tree leaf-split semantics, the separator key promoted to the parent
	// IS duplicated: it remains present as right.Keys[0] in the leaf itself.
	left, right := splitLeaf(newLeaf)
	rightID, err := alloc.Next()
	if err != nil {
		return 0, err
	}
	left.NextLeaf = rightID
	if err := writeLeaf(store, leafID, left); err != nil {
		return 0, err
	}
	if err := writeLeaf(store, rightID, right); err != nil {
		return 0, err
	}

	return propagateSplit(store, alloc, chain[:len(chain)-1], leafID, right.Keys[0], rightID)
}

// propagateSplit walks parentChain (root..parent-of-split-node, in that
// order, NOT including the node that was just split) from the bottom
// (nearest parent) upward, inserting (promotedKey, newChildID) into each
// ancestor immediately after the slot that used to point at
// childIDBeingReplaced (whose node ID never changes across a split -- only
// the "right" half ever gets a new ID). If an ancestor overflows in turn, it
// is split too (median key promoted alone, NOT duplicated, per standard
// internal-node split semantics) and propagation continues one level up. If
// propagation exhausts parentChain without finding room (i.e. the old root
// itself was split, or there was no parent at all -- a single leaf tree
// splitting), a brand-new root is allocated.
func propagateSplit(store *NodeStore, alloc *NodeAllocator, parentChain []uint64, childIDBeingReplaced uint64, promotedKey string, newChildID uint64) (newRootNodeID uint64, err error) {
	for i := len(parentChain) - 1; i >= 0; i-- {
		parentID := parentChain[i]
		isLeaf, _, parent, err := store.ReadNode(parentID)
		if err != nil {
			return 0, err
		}
		if isLeaf {
			return 0, fmt.Errorf("btree: internal invariant violated: ancestor node %d decoded as a leaf", parentID)
		}

		j := indexOfChild(parent.Children, childIDBeingReplaced)
		if j < 0 {
			return 0, fmt.Errorf("btree: internal invariant violated: parent node %d has no child pointer to %d", parentID, childIDBeingReplaced)
		}

		newParent := insertIntoInternal(parent, j, promotedKey, newChildID)
		if internalEncodedSize(newParent) <= NodeSize {
			if err := writeInternal(store, parentID, newParent); err != nil {
				return 0, err
			}
			return rootFromChain(parentChain), nil
		}

		// Ancestor overflowed: split it. The left half keeps the original
		// node ID; the right half gets a newly allocated ID. The median key
		// is promoted ALONE (not duplicated in either child) to the next
		// level up -- this is what distinguishes an internal-node split from
		// a leaf split.
		left, promoted, right := splitInternal(newParent)
		rightInternalID, err := alloc.Next()
		if err != nil {
			return 0, err
		}
		if err := writeInternal(store, parentID, left); err != nil {
			return 0, err
		}
		if err := writeInternal(store, rightInternalID, right); err != nil {
			return 0, err
		}

		childIDBeingReplaced = parentID
		promotedKey = promoted
		newChildID = rightInternalID
	}

	// Propagation reached past the top of parentChain: either the old root
	// itself just split, or there was no parent at all (a single-leaf tree
	// splitting for the first time). Either way, allocate a brand-new root.
	newRootID, err := alloc.Next()
	if err != nil {
		return 0, err
	}
	newRoot := InternalNode{Keys: []string{promotedKey}, Children: []uint64{childIDBeingReplaced, newChildID}}
	if err := writeInternal(store, newRootID, newRoot); err != nil {
		return 0, err
	}
	return newRootID, nil
}

// rootFromChain returns the root node ID given the (possibly empty) chain of
// ancestor node IDs above a split point (parentChain[0], if present, is
// always the tree's root and never changes when only a strict descendant
// split). If parentChain is empty, the leaf itself was the root, which is
// handled by propagateSplit falling through to the new-root case instead of
// calling this helper.
func rootFromChain(parentChain []uint64) uint64 {
	return parentChain[0]
}

// indexOfChild returns the index of target within children, or -1 if not
// present.
func indexOfChild(children []uint64, target uint64) int {
	for i, c := range children {
		if c == target {
			return i
		}
	}
	return -1
}

// insertIntoLeaf returns a copy of leaf with (key, fileID) inserted at sorted
// position i (as computed by the caller via sort.SearchStrings). Caller must
// have already confirmed key is not already present at i.
func insertIntoLeaf(leaf LeafNode, i int, key string, fileID uint64) LeafNode {
	keys := make([]string, 0, len(leaf.Keys)+1)
	keys = append(keys, leaf.Keys[:i]...)
	keys = append(keys, key)
	keys = append(keys, leaf.Keys[i:]...)

	fileIDs := make([]uint64, 0, len(leaf.FileIDs)+1)
	fileIDs = append(fileIDs, leaf.FileIDs[:i]...)
	fileIDs = append(fileIDs, fileID)
	fileIDs = append(fileIDs, leaf.FileIDs[i:]...)

	return LeafNode{Keys: keys, FileIDs: fileIDs, NextLeaf: leaf.NextLeaf, Version: leaf.Version}
}

// chooseLeafSplit picks the index at which to split an overflowing leaf's
// Keys/FileIDs (left = [:mid], right = [mid:]) so that both resulting halves
// fit within NodeSize. It starts from a half-by-key-count split, the natural
// choice when NodeSize (4096 bytes) is large relative to individual
// topic-path key lengths, and defensively shifts the split point using
// leafEncodedSize if that doesn't already fit (e.g. very uneven key
// lengths).
func chooseLeafSplit(n LeafNode) int {
	mid := len(n.Keys) / 2
	if mid < 1 {
		mid = 1
	}
	for mid > 1 && leafEncodedSize(LeafNode{Keys: n.Keys[:mid], FileIDs: n.FileIDs[:mid]}) > NodeSize {
		mid--
	}
	for mid < len(n.Keys)-1 && leafEncodedSize(LeafNode{Keys: n.Keys[mid:], FileIDs: n.FileIDs[mid:]}) > NodeSize {
		mid++
	}
	return mid
}

// splitLeaf splits an overflowing leaf into a left and right half at the
// index chosen by chooseLeafSplit. The separator key (right.Keys[0]) remains
// present in the leaf itself -- callers are responsible for promoting a copy
// of it to the parent, per standard B+Tree leaf-split semantics (the
// separator key IS duplicated for leaf splits, unlike internal-node splits).
// NextLeaf pointers are left zero-valued; callers must wire them up (left's
// NextLeaf -> right's node ID, right's NextLeaf -> the original leaf's old
// NextLeaf) once node IDs are known.
func splitLeaf(n LeafNode) (left, right LeafNode) {
	mid := chooseLeafSplit(n)
	left = LeafNode{
		Keys:    append([]string(nil), n.Keys[:mid]...),
		FileIDs: append([]uint64(nil), n.FileIDs[:mid]...),
	}
	right = LeafNode{
		Keys:     append([]string(nil), n.Keys[mid:]...),
		FileIDs:  append([]uint64(nil), n.FileIDs[mid:]...),
		NextLeaf: n.NextLeaf,
	}
	return left, right
}

// insertIntoInternal returns a copy of parent with promotedKey inserted at
// Keys[childIndex] and newChildID inserted at Children[childIndex+1],
// preserving the Children[i] covers [Keys[i-1], Keys[i]) invariant: the new
// child is placed immediately after the existing child (at childIndex) that
// used to cover the now-split range.
func insertIntoInternal(parent InternalNode, childIndex int, promotedKey string, newChildID uint64) InternalNode {
	keys := make([]string, 0, len(parent.Keys)+1)
	keys = append(keys, parent.Keys[:childIndex]...)
	keys = append(keys, promotedKey)
	keys = append(keys, parent.Keys[childIndex:]...)

	children := make([]uint64, 0, len(parent.Children)+1)
	children = append(children, parent.Children[:childIndex+1]...)
	children = append(children, newChildID)
	children = append(children, parent.Children[childIndex+1:]...)

	return InternalNode{Keys: keys, Children: children, Version: parent.Version}
}

// chooseInternalSplit picks the index of the median key to promote when
// splitting an overflowing internal node (left = Keys[:mid]/Children[:mid+1],
// promoted = Keys[mid], right = Keys[mid+1:]/Children[mid+1:]), analogous to
// chooseLeafSplit but verified against internalEncodedSize.
func chooseInternalSplit(n InternalNode) int {
	mid := len(n.Keys) / 2
	for mid > 0 && internalEncodedSize(InternalNode{Keys: n.Keys[:mid], Children: n.Children[:mid+1]}) > NodeSize {
		mid--
	}
	for mid < len(n.Keys)-1 && internalEncodedSize(InternalNode{Keys: n.Keys[mid+1:], Children: n.Children[mid+1:]}) > NodeSize {
		mid++
	}
	return mid
}

// splitInternal splits an overflowing internal node into a left half, a
// promoted median key, and a right half, per standard B+Tree internal-node
// split semantics: the median key is removed from both children and
// promoted alone to the next level up (NOT duplicated into either half --
// this is what distinguishes an internal split from a leaf split).
func splitInternal(n InternalNode) (left InternalNode, promoted string, right InternalNode) {
	mid := chooseInternalSplit(n)
	left = InternalNode{
		Keys:     append([]string(nil), n.Keys[:mid]...),
		Children: append([]uint64(nil), n.Children[:mid+1]...),
	}
	promoted = n.Keys[mid]
	right = InternalNode{
		Keys:     append([]string(nil), n.Keys[mid+1:]...),
		Children: append([]uint64(nil), n.Children[mid+1:]...),
	}
	return left, promoted, right
}

// writeLeaf encodes and writes leaf at nodeID.
func writeLeaf(store *NodeStore, nodeID uint64, leaf LeafNode) error {
	encoded, err := leaf.Encode()
	if err != nil {
		return err
	}
	return store.WriteNode(nodeID, encoded)
}

// writeInternal encodes and writes internal at nodeID.
func writeInternal(store *NodeStore, nodeID uint64, internal InternalNode) error {
	encoded, err := internal.Encode()
	if err != nil {
		return err
	}
	return store.WriteNode(nodeID, encoded)
}
