package btree

import (
	"fmt"
	"sort"
)

// Delete removes path from the B+Tree rooted at rootNodeID, reading and
// writing nodes via store. It returns found=false (with a nil error) if path
// is genuinely absent -- either because the tree is empty or because it was
// never inserted -- mirroring Lookup's existing "not-found is a normal,
// expected outcome, not an error" convention. A non-nil error indicates a
// genuine I/O or decode failure, or a violated internal structural
// invariant.
//
// alloc is accepted for API symmetry with Insert (and in case a future
// revision adds free-list reuse of abandoned node IDs, see "Abandoned node
// IDs" below) but is currently unused: Delete never allocates a new node, it
// only rewrites or repurposes existing ones.
//
// # Simplified rebalancing strategy (documented choice, per subtask 1.2.4)
//
// docs/LLD/btree.md defines Delete as one of the four core B+Tree operations
// but -- being scaffold-only at the time of this subtask -- does not specify
// a numeric underflow threshold, a merge-vs-redistribute preference, or a
// root-collapse convention. The issue's own checklist item for this subtask
// suggests a "merge-or-tombstone strategy, documented choice"; this
// implementation adopts exactly that:
//
//   - Tombstone policy: a leaf (or internal node) that still holds >= 1 key
//     after a deletion is left alone, even if under-capacity ("half full" or
//     less). No eager rebalancing is triggered on partial underflow.
//   - Repair trigger: rebalancing (borrow-or-merge) is triggered only when a
//     node becomes completely empty of keys (0 keys) as a direct result of a
//     deletion.
//   - Leaf repair (see repairEmptyLeaf): try to borrow one key from the left
//     sibling (if it has more than 1 key), else the right sibling (if it has
//     more than 1 key), else merge with a sibling (left preferred), fixing up
//     NextLeaf links and the parent's separator key / child pointer.
//   - Internal repair (see shrinkParentAfterMerge): only reachable when a
//     leaf merge causes the leaf's parent to drop from 1 key (2 children) to
//     0 keys (1 child) -- i.e. genuinely degenerate. That parent is spliced
//     out of ITS parent (the grandparent) by redirecting the grandparent's
//     child pointer straight to the degenerate parent's single surviving
//     child. This never changes the grandparent's own key/child count, so no
//     further propagation above that point is ever required: rebalancing is
//     bounded to at most two hops above the leaf (the leaf's parent may
//     shrink by one key/child, and then at most one further splice at the
//     grandparent, or a root collapse if the leaf's parent was itself the
//     root).
//   - Root collapse: if the root itself is the node that shrinks to 0 keys (1
//     child), that child becomes the new root, returned as newRootNodeID.
//   - Empty-tree-after-delete convention: if the leaf being deleted from IS
//     the root (a single-node tree) and it becomes empty, rootNodeID is
//     returned UNCHANGED (it still points at a valid, zero-key leaf) -- NOT
//     reset to reservedNodeID. This is a deliberate divergence from Insert's
//     "rootNodeID == reservedNodeID means bootstrap a brand-new tree"
//     convention: Delete's caller already has a real root node ID, and
//     resetting it to reservedNodeID here would force callers to distinguish
//     "never had a tree" from "tree is now empty" via some other signal,
//     which is not required by this subtask's acceptance criteria.
//
// # Abandoned node IDs (known gap, documented)
//
// NodeAllocator (1.2.3) only allocates monotonically increasing IDs and has
// no free-list. Node IDs eliminated by a merge or a grandparent splice are
// simply abandoned: never reused, never explicitly reclaimed. This mirrors
// 1.2.3's own documented known gap on NodeAllocator and is accepted as such
// here too; it is expected to be revisited once persist/reload (1.2.5/1.2.6)
// is built and a real on-disk free-list becomes worth the complexity.
func Delete(store *NodeStore, alloc *NodeAllocator, rootNodeID uint64, path string) (newRootNodeID uint64, found bool, err error) {
	_ = alloc // see doc comment: accepted but currently unused.

	if rootNodeID == reservedNodeID {
		return reservedNodeID, false, nil
	}

	chain, leaf, err := descendToLeaf(store, rootNodeID, path)
	if err != nil {
		return 0, false, err
	}
	leafID := chain[len(chain)-1]

	i := sort.SearchStrings(leaf.Keys, path)
	if i >= len(leaf.Keys) || leaf.Keys[i] != path {
		return rootNodeID, false, nil
	}

	newLeaf := removeFromLeaf(leaf, i)
	if err := writeLeaf(store, leafID, newLeaf); err != nil {
		return 0, false, err
	}

	if len(chain) == 1 {
		// The leaf IS the root (single-node tree): nothing above it to
		// repair even if newLeaf is now completely empty. See the
		// "empty-tree-after-delete convention" in the doc comment above.
		return rootNodeID, true, nil
	}

	if len(newLeaf.Keys) > 0 {
		// Tombstone policy: a leaf that still holds at least one key is left
		// alone, even if under-capacity. No rebalancing triggered.
		return rootNodeID, true, nil
	}

	newRootNodeID, err = repairEmptyLeaf(store, chain, newLeaf.NextLeaf)
	if err != nil {
		return 0, false, err
	}
	return newRootNodeID, true, nil
}

// removeFromLeaf returns a copy of leaf with the entry at sorted position i
// removed. Caller must have already confirmed leaf.Keys[i] == the key being
// removed.
func removeFromLeaf(leaf LeafNode, i int) LeafNode {
	keys := make([]string, 0, len(leaf.Keys)-1)
	keys = append(keys, leaf.Keys[:i]...)
	keys = append(keys, leaf.Keys[i+1:]...)

	fileIDs := make([]uint64, 0, len(leaf.FileIDs)-1)
	fileIDs = append(fileIDs, leaf.FileIDs[:i]...)
	fileIDs = append(fileIDs, leaf.FileIDs[i+1:]...)

	return LeafNode{Keys: keys, FileIDs: fileIDs, NextLeaf: leaf.NextLeaf, Version: leaf.Version}
}

// repairEmptyLeaf is called once chain's leaf (chain[len(chain)-1], already
// rewritten to disk as a zero-key LeafNode by the caller) has become
// completely empty as a direct result of a deletion, and chain has at least
// one ancestor (len(chain) >= 2, i.e. the leaf is not the root). emptyNextLeaf
// is the (now-empty) leaf's NextLeaf pointer, which must be preserved by
// whichever repair path runs (borrow leaves NextLeaf chains as-is except for
// the borrowing leaf's own single-key content; merge splices the empty leaf
// out of the NextLeaf chain entirely).
//
// It returns the tree's root node ID (unchanged, unless a root collapse
// occurred).
func repairEmptyLeaf(store *NodeStore, chain []uint64, emptyNextLeaf uint64) (uint64, error) {
	leafID := chain[len(chain)-1]
	parentIdx := len(chain) - 2
	parentID := chain[parentIdx]

	isLeaf, _, parent, err := store.ReadNode(parentID)
	if err != nil {
		return 0, err
	}
	if isLeaf {
		return 0, fmt.Errorf("btree: internal invariant violated: ancestor node %d decoded as a leaf", parentID)
	}

	j := indexOfChild(parent.Children, leafID)
	if j < 0 {
		return 0, fmt.Errorf("btree: internal invariant violated: parent node %d has no child pointer to %d", parentID, leafID)
	}
	if len(parent.Children) < 2 {
		// Structurally unreachable for a well-formed tree: an internal node
		// always has >= 2 children. Defensive guard against a panic if this
		// invariant is ever violated.
		return 0, fmt.Errorf("btree: internal invariant violated: parent node %d has only one child, cannot rebalance", parentID)
	}

	// Read whichever same-parent siblings exist, once, and record whether
	// each one actually decodes as a LeafNode.
	//
	// A same-parent sibling of an emptied leaf is not guaranteed to be a
	// leaf itself: Delete's own grandparent-splice repair (see
	// shrinkParentAfterMerge) can promote a bare surviving leaf up to sit
	// directly under a grandparent, alongside sibling children that are
	// themselves internal nodes (a shape pure Insert never produces).
	// assertStructuralInvariants (insert_test.go) does not require uniform
	// leaf depth, so this shape is not itself a violated structural
	// invariant -- but it does mean a leaf must never borrow-from or
	// merge-with a same-parent sibling of the wrong type: naively decoding
	// an INTERNAL sibling's bytes as a LeafNode yields a zero-valued
	// LeafNode, and "merging" with it (as the previous implementation did)
	// silently splices that sibling's separator key/child pointer out of
	// the parent -- permanently detaching that sibling's entire live
	// subtree with no error and no detectable structural violation. This
	// was a confirmed, reproducible silent data-loss bug (see
	// .cdr/runs/2026-07-04/028-verification/verification.json).
	//
	// Fix: only ever treat a sibling as a borrow/merge candidate if
	// store.ReadNode reports isLeaf==true for it. A same-parent sibling
	// that turns out to be internal is simply skipped as a candidate on
	// that side. If this leaves neither side usable, the emptied leaf is
	// left in place (already written as a zero-key LeafNode by Delete)
	// with the underflow unresolved, rather than ever merging/splicing
	// through a type mismatch.
	var (
		leftID, rightID     uint64
		left, right         LeafNode
		haveLeft, haveRight bool
	)
	if j > 0 {
		leftID = parent.Children[j-1]
		leftIsLeaf, l, _, lErr := store.ReadNode(leftID)
		if lErr != nil {
			return 0, lErr
		}
		if leftIsLeaf {
			left, haveLeft = l, true
		}
	}
	if j < len(parent.Children)-1 {
		rightID = parent.Children[j+1]
		rightIsLeaf, r, _, rErr := store.ReadNode(rightID)
		if rErr != nil {
			return 0, rErr
		}
		if rightIsLeaf {
			right, haveRight = r, true
		}
	}

	// Try to borrow one key from the left sibling, if it is a real leaf
	// sibling with more than one key to spare.
	if haveLeft && len(left.Keys) > 1 {
		borrowedKey := left.Keys[len(left.Keys)-1]
		borrowedFileID := left.FileIDs[len(left.FileIDs)-1]

		newLeft := LeafNode{
			Keys:     append([]string(nil), left.Keys[:len(left.Keys)-1]...),
			FileIDs:  append([]uint64(nil), left.FileIDs[:len(left.FileIDs)-1]...),
			NextLeaf: leafID,
		}
		newCurrent := LeafNode{
			Keys:     []string{borrowedKey},
			FileIDs:  []uint64{borrowedFileID},
			NextLeaf: emptyNextLeaf,
		}
		newParentKeys := append([]string(nil), parent.Keys...)
		newParentKeys[j-1] = borrowedKey
		newParent := InternalNode{Keys: newParentKeys, Children: append([]uint64(nil), parent.Children...)}

		if err := writeLeaf(store, leftID, newLeft); err != nil {
			return 0, err
		}
		if err := writeLeaf(store, leafID, newCurrent); err != nil {
			return 0, err
		}
		if err := writeInternal(store, parentID, newParent); err != nil {
			return 0, err
		}
		return chain[0], nil
	}

	// Try to borrow one key from the right sibling, if it is a real leaf
	// sibling with more than one key to spare.
	if haveRight && len(right.Keys) > 1 {
		borrowedKey := right.Keys[0]
		borrowedFileID := right.FileIDs[0]

		newRight := LeafNode{
			Keys:     append([]string(nil), right.Keys[1:]...),
			FileIDs:  append([]uint64(nil), right.FileIDs[1:]...),
			NextLeaf: right.NextLeaf,
		}
		newCurrent := LeafNode{
			Keys:     []string{borrowedKey},
			FileIDs:  []uint64{borrowedFileID},
			NextLeaf: rightID,
		}
		newParentKeys := append([]string(nil), parent.Keys...)
		newParentKeys[j] = newRight.Keys[0]
		newParent := InternalNode{Keys: newParentKeys, Children: append([]uint64(nil), parent.Children...)}

		if err := writeLeaf(store, rightID, newRight); err != nil {
			return 0, err
		}
		if err := writeLeaf(store, leafID, newCurrent); err != nil {
			return 0, err
		}
		if err := writeInternal(store, parentID, newParent); err != nil {
			return 0, err
		}
		return chain[0], nil
	}

	// Neither sibling can spare a key for a borrow: merge, but only with a
	// same-parent sibling that is actually a leaf. Prefer merging into the
	// left sibling (the empty leaf's node ID is abandoned); otherwise merge
	// the right sibling into the (empty) current leaf's node ID (the right
	// sibling's node ID is abandoned instead).
	if haveLeft {
		mergedLeft := LeafNode{
			Keys:     left.Keys,
			FileIDs:  left.FileIDs,
			NextLeaf: emptyNextLeaf,
		}
		if err := writeLeaf(store, leftID, mergedLeft); err != nil {
			return 0, err
		}
		// leafID is abandoned; see "Abandoned node IDs" in Delete's doc
		// comment.
		return shrinkParentAfterMerge(store, chain, parentIdx, j)
	}

	if haveRight {
		mergedCurrent := LeafNode{
			Keys:     right.Keys,
			FileIDs:  right.FileIDs,
			NextLeaf: right.NextLeaf,
		}
		if err := writeLeaf(store, leafID, mergedCurrent); err != nil {
			return 0, err
		}
		// rightID is abandoned; see "Abandoned node IDs" in Delete's doc
		// comment.
		return shrinkParentAfterMerge(store, chain, parentIdx, j+1)
	}

	// Neither same-parent sibling is a usable (same-type, i.e. leaf)
	// borrow/merge candidate -- both existing siblings are internal nodes,
	// the pathological post-grandparent-splice shape described above.
	// Rather than ever merge/splice through a type mismatch (which would
	// silently discard a live subtree), accept the underflow: the leaf
	// stays in place as the empty (0-key) LeafNode already written by
	// Delete's caller. It contributes no keys to the leaf-chain traversal
	// and remains structurally valid (reachable, correctly linked via
	// NextLeaf), just under-capacity -- consistent with this subtask's own
	// tombstone policy for non-empty underflow.
	return chain[0], nil
}

// shrinkParentAfterMerge removes the child pointer at removedChildIdx (an
// abandoned node ID that a leaf-level or internal-level merge just spliced
// out) and its associated separator key (at removedChildIdx-1, per the
// standard n-keys/n+1-children convention) from chain[parentIdx]'s decoded
// InternalNode, then re-reads and rewrites it.
//
// If chain[parentIdx] is the tree's root and this shrink leaves it with only
// one child (0 keys), the root collapses: that single child becomes the new
// root, returned as this function's result. Otherwise, if the shrink leaves a
// non-root ancestor with only one child (0 keys), that ancestor is itself
// degenerate and is spliced out of ITS parent (chain[parentIdx-1]) by
// redirecting the grandparent's child pointer straight to the degenerate
// ancestor's single surviving child -- this never changes the grandparent's
// own key/child count, so no further propagation above that point is ever
// needed (see Delete's doc comment for the full rationale).
func shrinkParentAfterMerge(store *NodeStore, chain []uint64, parentIdx int, removedChildIdx int) (uint64, error) {
	parentID := chain[parentIdx]
	_, _, parent, err := store.ReadNode(parentID)
	if err != nil {
		return 0, err
	}

	newKeys := make([]string, 0, len(parent.Keys)-1)
	newKeys = append(newKeys, parent.Keys[:removedChildIdx-1]...)
	newKeys = append(newKeys, parent.Keys[removedChildIdx:]...)

	newChildren := make([]uint64, 0, len(parent.Children)-1)
	newChildren = append(newChildren, parent.Children[:removedChildIdx]...)
	newChildren = append(newChildren, parent.Children[removedChildIdx+1:]...)

	newParent := InternalNode{Keys: newKeys, Children: newChildren}

	if parentIdx == 0 {
		if len(newParent.Children) == 1 {
			// Root collapse: the single remaining child becomes the new
			// root. The old root node ID is abandoned.
			return newParent.Children[0], nil
		}
		if err := writeInternal(store, parentID, newParent); err != nil {
			return 0, err
		}
		return parentID, nil
	}

	if err := writeInternal(store, parentID, newParent); err != nil {
		return 0, err
	}

	if len(newParent.Children) > 1 {
		// Parent still has at least one key: tombstone policy, no further
		// propagation needed.
		return chain[0], nil
	}

	// Parent became degenerate (0 keys, 1 child): splice it out of ITS
	// parent (the grandparent) by replacing the grandparent's pointer to
	// parentID with parent's single surviving child.
	grandParentIdx := parentIdx - 1
	grandParentID := chain[grandParentIdx]
	_, _, grandParent, err := store.ReadNode(grandParentID)
	if err != nil {
		return 0, err
	}
	gj := indexOfChild(grandParent.Children, parentID)
	if gj < 0 {
		return 0, fmt.Errorf("btree: internal invariant violated: node %d has no child pointer to %d", grandParentID, parentID)
	}
	newGrandChildren := append([]uint64(nil), grandParent.Children...)
	newGrandChildren[gj] = newParent.Children[0]
	newGrandParent := InternalNode{Keys: append([]string(nil), grandParent.Keys...), Children: newGrandChildren}
	if err := writeInternal(store, grandParentID, newGrandParent); err != nil {
		return 0, err
	}

	return chain[0], nil
}
