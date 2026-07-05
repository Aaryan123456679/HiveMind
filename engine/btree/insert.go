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
		left.NextSibling = rightInternalID
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

	return InternalNode{Keys: keys, Children: children, Version: parent.Version, NextSibling: parent.NextSibling, LowKey: parent.LowKey}
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
//
// right.NextSibling is set to n's own (pre-split) NextSibling, preserving
// the existing right-link chain (mirrors splitLeaf's right.NextLeaf =
// n.NextLeaf); left.NextSibling is left zero-valued -- callers are
// responsible for wiring left.NextSibling -> right's newly allocated node
// ID once it is known, exactly as they already do for leaf splits'
// left.NextLeaf. See InternalNode.NextSibling's doc comment (node.go) for
// why this field exists (2a.4.2's move-right latch-crabbing recovery).
func splitInternal(n InternalNode) (left InternalNode, promoted string, right InternalNode) {
	mid := chooseInternalSplit(n)
	left = InternalNode{
		Keys:     append([]string(nil), n.Keys[:mid]...),
		Children: append([]uint64(nil), n.Children[:mid+1]...),
		LowKey:   n.LowKey,
	}
	promoted = n.Keys[mid]
	right = InternalNode{
		Keys:        append([]string(nil), n.Keys[mid+1:]...),
		Children:    append([]uint64(nil), n.Children[mid+1:]...),
		NextSibling: n.NextSibling,
		LowKey:      promoted,
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

// Tree is the concurrency-safe entry point for latch-crabbing insert
// (2a.4.2) and, later, delete (2a.4.3). It wraps a *NodeStore and
// *NodeAllocator (both already safe for concurrent use per 2a.4.1) together
// with one piece of NEW tree-level state: which node ID is currently the
// tree's root.
//
// Why root needs its own mutex, separate from any per-node nodeLatch
// (latch.go): "which node ID is the root right now" has no on-disk node
// identity of its own -- it is purely in-memory application state -- so it
// cannot be protected by locking any individual node's latch. It also
// changes far less often than any individual node's content (only on a
// root split or the very first insert into an empty tree), so a single
// dedicated sync.Mutex (rootMu), held only very briefly around those rare
// events, is deliberately kept separate from the high-traffic per-node
// latch registry: this keeps concurrent inserts into disjoint subtrees from
// ever contending on rootMu at all, except during the rare instant a root
// split is actually happening.
//
// The pre-existing free function Insert (above) is left completely
// unmodified: it remains the single-threaded entry point relied on by all
// of 1.2.x/2a.x's existing tests. Tree is purely additive.
type Tree struct {
	Store *NodeStore
	Alloc *NodeAllocator

	// rootMu guards root. See the Tree doc comment above for why this is a
	// dedicated mutex rather than reusing Store's per-node latch registry.
	rootMu sync.Mutex
	root   uint64
}

// NewTree wraps store/alloc plus rootNodeID (which may be reservedNodeID
// for a brand-new, still-empty tree) into a Tree ready for concurrent
// Insert calls.
func NewTree(store *NodeStore, alloc *NodeAllocator, rootNodeID uint64) *Tree {
	return &Tree{Store: store, Alloc: alloc, root: rootNodeID}
}

// Root returns the tree's current root node ID, safe for concurrent use
// with in-flight Insert calls.
func (t *Tree) Root() uint64 {
	t.rootMu.Lock()
	defer t.rootMu.Unlock()
	return t.root
}

// Insert inserts path -> fileID into t using latch-crabbing (2a.4.2): at
// all times this call holds at most a parent-and-child pair of per-node
// latches (see crabInsert/propagate's doc comments for the exact
// discipline), so concurrent Insert calls into disjoint subtrees never
// block each other, while concurrent Insert calls that do touch the same
// node(s) are correctly serialized node-by-node.
//
// If path is already present, its fileID is updated in place (upsert
// semantics, matching the free Insert function's convention).
func (t *Tree) Insert(path string, fileID uint64) error {
	t.rootMu.Lock()
	if t.root == reservedNodeID {
		// Empty-tree bootstrap, held under rootMu for its entire (rare,
		// one-shot) duration: a second concurrent bootstrapper simply
		// blocks on rootMu and, once unblocked, observes the now-installed
		// root and falls through to the normal path below instead of
		// racing to create a second root.
		leafID, err := t.Alloc.Next()
		if err != nil {
			t.rootMu.Unlock()
			return err
		}
		leaf := LeafNode{Keys: []string{path}, FileIDs: []uint64{fileID}, NextLeaf: noSibling}
		t.Store.Lock(leafID)
		err = writeLeaf(t.Store, leafID, leaf)
		t.Store.Unlock(leafID)
		if err != nil {
			t.rootMu.Unlock()
			return err
		}
		t.root = leafID
		t.rootMu.Unlock()
		return nil
	}
	root := t.root
	t.rootMu.Unlock()

	return t.crabInsert(root, path, fileID)
}

// crabInsert descends from rootID toward path using the window-of-2
// latch-crabbing discipline mandated by this subtask's acceptance criteria:
// lock the child, THEN release the parent, before locking the grandchild --
// never hold more than {parent, child} at once. rootID's own latch is held
// alone (window of 1) at the very start, growing to 2 only for the brief
// instant a child is locked just before its parent is released.
//
// At every node visited (internal or leaf), crabInsert first applies the
// "move right on overshoot" recovery described on InternalNode.NextSibling
// (node.go): releasing a parent's latch before a split it may be about to
// cause has been propagated back up to it means a concurrent writer can be
// routed, via that momentarily-stale parent, to a node whose upper key
// range has already been split off into a new right sibling. Detecting
// that (path is greater than every key currently in the node, and the node
// has a right sibling) and moving right before making any further routing
// decision is what makes the window-of-2 discipline safe -- without it, an
// insert could land in a node that a lookup would never reach once the
// split is eventually propagated, silently losing data. See findParent for
// the identical recovery applied during split propagation.
//
// Note that rootID need not still be the tree's actual root by the time
// this call reaches the leaf level and (possibly) needs to propagate a
// split upward past rootID -- see propagate's doc comment for how that rare
// race is handled; crabInsert itself does not need to know or care, since
// it only ever descends downward from rootID, and nothing about rootID's
// own subtree changes based on what (if anything) sits above it.
func (t *Tree) crabInsert(rootID uint64, path string, fileID uint64) error {
	store := t.Store

	store.Lock(rootID)
	currentID := rootID
	for {
		isLeaf, leaf, internal, err := store.ReadNode(currentID)
		if err != nil {
			store.Unlock(currentID)
			return err
		}

		if isLeaf {
			for leaf.NextLeaf != noSibling {
				nextID := leaf.NextLeaf
				store.Lock(nextID)
				nextIsLeaf, nextLeaf, _, err := store.ReadNode(nextID)
				if err != nil {
					store.Unlock(nextID)
					store.Unlock(currentID)
					return err
				}
				if !nextIsLeaf {
					store.Unlock(nextID)
					store.Unlock(currentID)
					return fmt.Errorf("btree: internal invariant violated: NextLeaf chain led to non-leaf node %d", nextID)
				}
				// Peek the sibling's own true lower bound (its first key --
				// leaves store real keys directly, so this is exact) rather
				// than comparing path against currentID's own currently
				// populated max key: a sparsely-filled node's max key can be
				// far below the true boundary between it and its sibling
				// whenever keys are inserted out of order, which would make
				// an own-max-key comparison move right too eagerly and
				// misroute a key that still belongs in currentID. See
				// InternalNode.LowKey's doc comment for the identical
				// reasoning at internal-node levels.
				if len(nextLeaf.Keys) > 0 && path < nextLeaf.Keys[0] {
					store.Unlock(nextID)
					break
				}
				store.Unlock(currentID)
				currentID = nextID
				leaf = nextLeaf
			}
			return t.insertIntoLeafAndPropagate(currentID, leaf, path, fileID)
		}

		for internal.NextSibling != noSibling {
			nextID := internal.NextSibling
			store.Lock(nextID)
			nextIsLeaf, _, nextInternal, err := store.ReadNode(nextID)
			if err != nil {
				store.Unlock(nextID)
				store.Unlock(currentID)
				return err
			}
			if nextIsLeaf {
				store.Unlock(nextID)
				store.Unlock(currentID)
				return fmt.Errorf("btree: internal invariant violated: NextSibling chain led to a leaf node %d", nextID)
			}
			// Peek the sibling's LowKey (its true subtree lower bound, fixed
			// forever since creation) rather than currentID's own currently
			// populated max separator -- see InternalNode.LowKey's doc
			// comment for why the latter is unsafe.
			if nextInternal.LowKey != "" && path < nextInternal.LowKey {
				store.Unlock(nextID)
				break
			}
			store.Unlock(currentID)
			currentID = nextID
			internal = nextInternal
		}

		i := sort.Search(len(internal.Keys), func(i int) bool { return path < internal.Keys[i] })
		childID := internal.Children[i]

		store.Lock(childID)
		store.Unlock(currentID)
		currentID = childID
	}
}

// insertIntoLeafAndPropagate performs the leaf-level mutation for
// crabInsert. The caller must already hold leafID's latch; this function
// releases it (and, if a split occurs, the freshly-allocated right
// sibling's latch too) before returning, on every path.
func (t *Tree) insertIntoLeafAndPropagate(leafID uint64, leaf LeafNode, path string, fileID uint64) error {
	store := t.Store

	i := sort.SearchStrings(leaf.Keys, path)
	if i < len(leaf.Keys) && leaf.Keys[i] == path {
		// Upsert: key already present, no structural change possible.
		leaf.FileIDs[i] = fileID
		err := writeLeaf(store, leafID, leaf)
		store.Unlock(leafID)
		return err
	}

	newLeaf := insertIntoLeaf(leaf, i, path, fileID)
	if leafEncodedSize(newLeaf) <= NodeSize {
		err := writeLeaf(store, leafID, newLeaf)
		store.Unlock(leafID)
		return err
	}

	// Leaf overflowed: split it, exactly as the single-threaded Insert
	// does. leafID (still held) keeps the left half; a freshly allocated
	// ID gets the right half.
	left, right := splitLeaf(newLeaf)
	rightID, err := t.Alloc.Next()
	if err != nil {
		store.Unlock(leafID)
		return err
	}
	left.NextLeaf = rightID

	werr := writeLeaf(store, leafID, left)
	if werr == nil {
		// rightID was just allocated by this call: no other goroutine can
		// possibly know about it yet, so locking it is uncontended, but is
		// done anyway for hygiene/consistency with the "Lock before
		// WriteNode" convention. This is the only point where 2 latches
		// (leafID + rightID) are held simultaneously, both already
		// accounted for within the parent-and-child budget.
		store.Lock(rightID)
		werr = writeLeaf(store, rightID, right)
		store.Unlock(rightID)
	}
	store.Unlock(leafID)
	if werr != nil {
		return werr
	}

	return t.propagate(leafID, right.Keys[0], rightID, path)
}

// findParent locates the CURRENT direct parent of childID by descending
// from rootID toward path using the same key-routing rule as
// descendToLeaf/crabInsert, applying the same window-of-2 crabbing
// discipline, and stopping at the first internal node whose Children slice
// actually contains childID.
//
// This works correctly even when childID's ancestry has been concurrently
// restructured (additional splits, or even a brand-new root promoted above
// it) since this call's caller last knew about it: node IDs are never
// reparented in this package -- a split only ever creates a brand-new
// *sibling* ID, never moves an existing ID to a different parent lineage --
// so childID is always still reachable via path's ordinary top-down
// key-routing from whatever the CURRENT root now is, no matter how many
// additional promotions or sibling splits have happened concurrently.
// findParent is what lets propagate uniformly handle both "the ordinary
// ancestor still exists, just relocate it" and "a concurrent root split
// happened, relocate the new ancestor chain above the old root" without two
// separate code paths.
//
// findParent returns an error only for a genuine I/O/decode failure or an
// invariant violation (reaching a leaf without ever finding childID as a
// listed child, which would mean childID is not actually reachable via
// path from rootID -- a caller bug, not a concurrency race).
func (t *Tree) findParent(rootID uint64, path string, childID uint64) (uint64, error) {
	store := t.Store

	store.Lock(rootID)
	currentID := rootID
	for {
		isLeaf, _, internal, err := store.ReadNode(currentID)
		if err != nil {
			store.Unlock(currentID)
			return 0, err
		}
		if isLeaf {
			store.Unlock(currentID)
			return 0, fmt.Errorf("btree: internal invariant violated: findParent reached leaf %d while searching for the current parent of %d along path %q", currentID, childID, path)
		}

		// Move-right recovery: see crabInsert's doc comment for why this is
		// required for window-of-2 crabbing to be safe. Applied here before
		// testing indexOfChild so this call always settles on the CURRENT
		// node that would legitimately contain childID, not a stale one
		// whose upper range (possibly including childID's own position in
		// the level below) has already been split off to the right.
		for internal.NextSibling != noSibling {
			nextID := internal.NextSibling
			store.Lock(nextID)
			nextIsLeaf, _, nextInternal, err := store.ReadNode(nextID)
			if err != nil {
				store.Unlock(nextID)
				store.Unlock(currentID)
				return 0, err
			}
			if nextIsLeaf {
				store.Unlock(nextID)
				store.Unlock(currentID)
				return 0, fmt.Errorf("btree: internal invariant violated: NextSibling chain led to a leaf node %d", nextID)
			}
			// See crabInsert's identical peek-the-sibling's-LowKey logic for
			// why this must not be based on internal's own currently
			// populated max separator.
			if nextInternal.LowKey != "" && path < nextInternal.LowKey {
				store.Unlock(nextID)
				break
			}
			store.Unlock(currentID)
			currentID = nextID
			internal = nextInternal
		}

		if indexOfChild(internal.Children, childID) >= 0 {
			store.Unlock(currentID)
			return currentID, nil
		}

		i := sort.Search(len(internal.Keys), func(i int) bool { return path < internal.Keys[i] })
		nextID := internal.Children[i]

		store.Lock(nextID)
		store.Unlock(currentID)
		currentID = nextID
	}
}

// propagate inserts (promotedKey, newChildID) -- replacing the child
// pointer that used to reference childIDBeingReplaced -- into
// childIDBeingReplaced's current direct parent, splitting that parent (and
// propagating further up) as needed, all the way up to and including a
// brand-new root if childIDBeingReplaced turns out to still be the tree's
// root. path is the ORIGINAL key being inserted by this call and is used
// only to ROUTE findParent's descent -- see findParent's doc comment for
// why this remains correct even after concurrent restructuring above
// childIDBeingReplaced.
//
// At most one ancestor's latch is held at a time across this walk (plus a
// transient second latch on a freshly-allocated, still-uncontended sibling
// when an ancestor itself overflows), well within the "parent+child" latch
// budget. Two concurrency races are handled here, both by retrying with a
// fresh lookup rather than trusting stale state:
//
//   - Concurrent root split: this call takes rootMu and checks whether
//     childIDBeingReplaced is STILL the tree's current root. If yes, it
//     allocates and installs a brand-new root, still under rootMu, so no
//     concurrent reader/writer of Tree.root can observe a half-updated
//     value. If another insert has, in the tiny window since this call
//     last checked, already promoted a different node above
//     childIDBeingReplaced, this check correctly fails and this call falls
//     back to findParent to relocate the (now taller) ancestor chain above
//     it instead of installing a conflicting second root.
//   - Concurrent shared-parent split: after findParent locates a parent and
//     this call locks it, indexOfChild might still fail (-1) if another
//     concurrent insert split that very parent, moving childIDBeingReplaced
//     into a new sibling, in the gap between findParent's read and this
//     call's Lock. This call detects that (rather than erroring out) and
//     simply retries: loops back to a fresh findParent call, which is
//     guaranteed to locate the correct current parent.
func (t *Tree) propagate(childIDBeingReplaced uint64, promotedKey string, newChildID uint64, path string) error {
	store := t.Store

	for {
		t.rootMu.Lock()
		if t.root == childIDBeingReplaced {
			newRootID, err := t.Alloc.Next()
			if err != nil {
				t.rootMu.Unlock()
				return err
			}
			newRoot := InternalNode{Keys: []string{promotedKey}, Children: []uint64{childIDBeingReplaced, newChildID}}

			store.Lock(newRootID)
			err = writeInternal(store, newRootID, newRoot)
			store.Unlock(newRootID)
			if err != nil {
				t.rootMu.Unlock()
				return err
			}

			t.root = newRootID
			t.rootMu.Unlock()
			return nil
		}
		currentRoot := t.root
		t.rootMu.Unlock()

		parentID, err := t.findParent(currentRoot, path, childIDBeingReplaced)
		if err != nil {
			return err
		}

		store.Lock(parentID)
		isLeaf, _, parent, err := store.ReadNode(parentID)
		if err != nil {
			store.Unlock(parentID)
			return err
		}
		if isLeaf {
			store.Unlock(parentID)
			return fmt.Errorf("btree: internal invariant violated: ancestor node %d decoded as a leaf", parentID)
		}

		j := indexOfChild(parent.Children, childIDBeingReplaced)
		if j < 0 {
			// Lost a race: parentID was itself split by a concurrent insert
			// between findParent's read and this Lock+ReadNode, moving
			// childIDBeingReplaced into a new sibling. Retry from the top
			// of the loop; findParent will relocate the correct current
			// parent (possibly the new sibling, possibly further up).
			store.Unlock(parentID)
			continue
		}

		newParent := insertIntoInternal(parent, j, promotedKey, newChildID)
		if internalEncodedSize(newParent) <= NodeSize {
			err := writeInternal(store, parentID, newParent)
			store.Unlock(parentID)
			return err
		}

		// Ancestor overflowed: split it too, same semantics as the
		// single-threaded propagateSplit (median key promoted alone).
		left, promoted, right := splitInternal(newParent)
		rightID, err := t.Alloc.Next()
		if err != nil {
			store.Unlock(parentID)
			return err
		}
		left.NextSibling = rightID

		werr := writeInternal(store, parentID, left)
		if werr == nil {
			store.Lock(rightID)
			werr = writeInternal(store, rightID, right)
			store.Unlock(rightID)
		}
		store.Unlock(parentID)
		if werr != nil {
			return werr
		}

		childIDBeingReplaced = parentID
		promotedKey = promoted
		newChildID = rightID
	}
}
