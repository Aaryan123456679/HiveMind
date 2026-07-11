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

// ---------------------------------------------------------------------------
// 2a.4.3: latch-crabbing concurrent delete.
//
// This section adds Tree.Delete, a concurrency-safe entry point for delete
// that reuses the exact TryLock + full-release + restart-from-root discipline
// established by 2a.4.2's insert (see insert.go's errRestartFromRoot doc
// comment for the full deadlock-avoidance argument, which applies unchanged
// here). It deliberately does NOT redesign the single-threaded Delete's
// merge-or-tombstone policy above -- only adds concurrency-safe locking
// around the same borrow/merge/root-collapse decisions.
//
// Lock-window discipline (see plan.md for the full writeup):
//   - Descent: window-of-2 (parent+child), TryLock-based, identical in shape
//     to crabInsert/findParent. Duplicated as crabDeleteOnce below rather
//     than refactoring crabInsertOnce, since the latter is not generic and
//     insert.go must not be touched (conservative, per this subtask's scope).
//   - Leaf-level borrow/merge: a WIDER, 3-latch window (parent, empty leaf,
//     chosen sibling) -- delete's genuine extra complexity vs insert, since
//     merging touches two sibling leaves plus their shared parent at once.
//     The 3rd latch is acquired via TryLock only (never blocking Lock),
//     with full release of all three and restart-from-root on a miss --
//     preserving deadlock-freedom by the same construction as insert.
//   - Ancestor cascade (parent degenerates to 0 keys/1 child, possible root
//     collapse): back to a 1-latch-at-a-time window, structurally identical
//     to propagate's climbing loop (rootMu check, fresh findParent, isolated
//     blocking Lock on the newly-located ancestor).
// ---------------------------------------------------------------------------

// Delete removes path from t using latch-crabbing (2a.4.3): at all times
// holds at most a parent-and-child pair of per-node latches during descent,
// briefly widening to a parent+leaf+sibling (3-node) window only for the
// leaf-level borrow/merge decision (see this section's doc comment above),
// so concurrent Delete/Insert calls into disjoint subtrees never block each
// other, while calls that touch the same node(s) are correctly serialized
// node-by-node.
//
// Mirrors the free Delete function's semantics: found=false (nil error)
// means path was genuinely absent, matching Lookup's "not-found is normal"
// convention. A non-nil error means genuine I/O/decode failure or a violated
// internal structural invariant.
func (t *Tree) Delete(path string) (found bool, err error) {
	t.rootMu.Lock()
	if t.root == reservedNodeID {
		t.rootMu.Unlock()
		return false, nil
	}
	root := t.root
	t.rootMu.Unlock()

	return t.crabDelete(root, path)
}

// crabDelete retries crabDeleteOnce from the root on every errRestartFromRoot
// (a hand-over-hand TryLock miss), with jittered backoff between attempts --
// identical retry shape to crabInsert/findParent, including the same
// crabMaxRestarts bound (see insert.go's doc comment on crabMaxRestarts):
// past that many consecutive restarts without a single attempt succeeding,
// this gives up and surfaces errTooManyRestarts rather than retrying
// forever. As with crabInsert, this is a theoretical, never-observed-in-
// practice defensive livelock guard, not a correctness fix -- every restart
// here happens strictly before any mutation for that attempt, so restarting
// any number of times is always structurally safe.
func (t *Tree) crabDelete(rootID uint64, path string) (bool, error) {
	for attempt := 0; ; attempt++ {
		if attempt >= crabMaxRestarts {
			return false, errTooManyRestarts
		}
		if attempt > 0 {
			crabRetryBackoff(attempt)
		}
		found, err := t.crabDeleteOnce(rootID, path)
		if err == errRestartFromRoot {
			restartFromRootCount.Add(1)
			continue
		}
		return found, err
	}
}

// crabDeleteOnce performs a single attempt at crabDelete's descent. It is a
// deliberate structural duplicate of crabInsertOnce's window-of-2 TryLock
// descent (including the identical NextLeaf/NextSibling move-right recovery
// for a node visited mid-split) -- not a refactor of it, since
// crabInsertOnce is insert-specific (it takes a fileID and calls
// insert-only leaf logic) and insert.go's now-hard-won-correct crabbing code
// is deliberately left untouched by this subtask. Every restart-worthy
// TryLock miss is entirely read-only up to this point (no mutation has
// happened yet), so restarting from scratch on a miss is always safe, exactly
// as documented on crabInsertOnce.
func (t *Tree) crabDeleteOnce(rootID uint64, path string) (bool, error) {
	store := t.Store

	store.Lock(rootID)
	currentID := rootID
	for {
		isLeaf, leaf, internal, err := store.ReadNode(currentID)
		if err != nil {
			store.Unlock(currentID)
			return false, err
		}

		if isLeaf {
			for {
				if len(leaf.Keys) == 0 || path >= leaf.Keys[0] {
					if leaf.NextLeaf == noSibling {
						break
					}
					nextID := leaf.NextLeaf
					if !store.TryLock(nextID) {
						store.Unlock(currentID)
						if crabRetryHook != nil {
							crabRetryHook(nextID)
						}
						return false, errRestartFromRoot
					}
					nextIsLeaf, nextLeaf, _, err := store.ReadNode(nextID)
					if err != nil {
						store.Unlock(nextID)
						store.Unlock(currentID)
						return false, err
					}
					if !nextIsLeaf {
						store.Unlock(nextID)
						store.Unlock(currentID)
						return false, fmt.Errorf("btree: internal invariant violated: NextLeaf chain led to non-leaf node %d", nextID)
					}
					// 2a.4.5 fix: an empty sibling must never be moved into --
					// see crabInsertOnce's identical fix (insert.go) for the
					// full root-cause writeup. A NextLeaf sibling that is
					// completely empty is, under Delete's tombstone policy, a
					// drained leaf awaiting its own repair, not a genuine
					// split-off right half; it carries no usable lower-bound
					// key of its own, so falling through to "move right"
					// whenever it happens to be empty would misroute this
					// delete into an unrelated, out-of-range leaf.
					if len(nextLeaf.Keys) == 0 || path < nextLeaf.Keys[0] {
						store.Unlock(nextID)
						break
					}
					store.Unlock(currentID)
					currentID = nextID
					leaf = nextLeaf
					continue
				}
				break
			}
			return t.deleteFromLeafAndRepair(currentID, leaf, path)
		}

		if internal.NextSibling != noSibling {
			nextID := internal.NextSibling
			if !store.TryLock(nextID) {
				store.Unlock(currentID)
				if crabRetryHook != nil {
					crabRetryHook(nextID)
				}
				return false, errRestartFromRoot
			}
			nextIsLeaf, _, nextInternal, err := store.ReadNode(nextID)
			if err != nil {
				store.Unlock(nextID)
				store.Unlock(currentID)
				return false, err
			}
			if nextIsLeaf {
				store.Unlock(nextID)
				store.Unlock(currentID)
				return false, fmt.Errorf("btree: internal invariant violated: NextSibling chain led to a leaf node %d", nextID)
			}
			if nextInternal.LowKey != "" && path >= nextInternal.LowKey {
				store.Unlock(currentID)
				currentID = nextID
				internal = nextInternal
				continue
			}
			store.Unlock(nextID)
		}

		i := sort.Search(len(internal.Keys), func(i int) bool { return path < internal.Keys[i] })
		childID := internal.Children[i]

		if !store.TryLock(childID) {
			store.Unlock(currentID)
			if crabRetryHook != nil {
				crabRetryHook(childID)
			}
			return false, errRestartFromRoot
		}
		store.Unlock(currentID)
		currentID = childID
	}
}

// deleteFromLeafAndRepair performs the leaf-level mutation for crabDelete.
// The caller must already hold leafID's latch; this function releases it
// (and, if a leaf-level borrow/merge occurs, every other latch it
// transitively acquires) before returning, on every path.
func (t *Tree) deleteFromLeafAndRepair(leafID uint64, leaf LeafNode, path string) (bool, error) {
	store := t.Store

	i := sort.SearchStrings(leaf.Keys, path)
	if i >= len(leaf.Keys) || leaf.Keys[i] != path {
		// Genuinely absent: no mutation happened, nothing to unwind.
		store.Unlock(leafID)
		return false, nil
	}

	newLeaf := removeFromLeaf(leaf, i)
	if err := writeLeaf(store, leafID, newLeaf); err != nil {
		store.Unlock(leafID)
		return false, err
	}

	if len(newLeaf.Keys) > 0 {
		// Tombstone policy: leaf still holds at least one key, left alone
		// even under-capacity. No rebalancing triggered.
		store.Unlock(leafID)
		return true, nil
	}

	// Leaf became empty. If it is currently the tree's root (single-node
	// tree), the empty-tree-after-delete convention applies: leave it as-is,
	// matching the single-threaded Delete's documented behavior.
	//
	// Lock-ordering note: this acquires rootMu while still holding leafID's
	// latch -- the REVERSE of insert.go's documented rootMu-first ordering.
	// This is safe: rootMu is always a wait-for-graph SINK in this package
	// (nothing that holds rootMu ever attempts to acquire a node latch while
	// holding it -- every rootMu-holding critical section here only reads or
	// writes t.root itself), so a cycle through rootMu is structurally
	// impossible regardless of which order any given call site acquires
	// rootMu vs. a node latch in. Do not add code inside a rootMu-held
	// section that blocks on a node latch, or this invariant breaks.
	t.rootMu.Lock()
	isRoot := t.root == leafID
	t.rootMu.Unlock()
	if isRoot {
		store.Unlock(leafID)
		return true, nil
	}

	store.Unlock(leafID)
	if err := t.repairEmptyLeaf(leafID, path); err != nil {
		return false, err
	}
	return true, nil
}

// repairEmptyLeaf rebalances (borrow-or-merge) leafID after it was emptied
// by a delete, mirroring the single-threaded repairEmptyLeaf's policy
// exactly, with concurrency-safe locking layered on top. path is the
// deleted key: still valid for findParent's routing purposes, since a
// node's location in the tree never moves once created (only new siblings
// are created alongside it -- see findParent's doc comment in insert.go).
func (t *Tree) repairEmptyLeaf(leafID uint64, path string) error {
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			crabRetryBackoff(attempt)
		}

		t.rootMu.Lock()
		if t.root == leafID {
			// Became root concurrently (e.g. a concurrent delete's root
			// collapse promoted this very leaf): tombstone convention for
			// an empty root leaf applies; nothing to repair.
			t.rootMu.Unlock()
			return nil
		}
		currentRoot := t.root
		t.rootMu.Unlock()

		parentID, err := t.findParent(currentRoot, path, leafID)
		if err != nil {
			return err
		}

		retry, err := t.repairEmptyLeafAtParent(parentID, leafID, path)
		if err == errRestartFromRoot {
			continue
		}
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
		// retry==true: a benign race (parent changed under us, or the leaf
		// was concurrently refilled) -- findParent will relocate the
		// current parent fresh on the next iteration.
	}
}

// repairEmptyLeafAtParent holds parentID's latch (acquired via a single,
// isolated blocking Lock -- nothing else is held at that point, mirroring
// propagate's identical Lock(parentID) call) and decides/performs the
// leaf-level borrow-or-merge, widening to a 3-latch window (parent, leaf,
// chosen sibling) only for the brief duration of that decision. Every
// widening beyond the first two latches uses TryLock, never blocking Lock;
// a miss releases everything currently held and returns errRestartFromRoot,
// exactly like every other hand-over-hand step in this package.
//
// retry==true (with a nil error) signals a benign, non-fatal race the
// caller should retry via a fresh findParent call: either parentID no
// longer has a child pointer to leafID (parentID was itself split or
// otherwise changed concurrently), or leafID was found already non-empty
// (refilled by a concurrent insert, or already repaired by a concurrent
// delete) by the time its latch was acquired here.
func (t *Tree) repairEmptyLeafAtParent(parentID, leafID uint64, path string) (retry bool, err error) {
	store := t.Store

	store.Lock(parentID)
	isLeafP, _, parent, err := store.ReadNode(parentID)
	if err != nil {
		store.Unlock(parentID)
		return false, err
	}
	if isLeafP {
		store.Unlock(parentID)
		return false, fmt.Errorf("btree: internal invariant violated: ancestor node %d decoded as a leaf", parentID)
	}

	j := indexOfChild(parent.Children, leafID)
	if j < 0 {
		// Race: parentID no longer points at leafID (e.g. parentID was
		// split concurrently). Retry: findParent will relocate the
		// current parent fresh.
		store.Unlock(parentID)
		return true, nil
	}
	if len(parent.Children) < 2 {
		// Structurally unreachable in a well-formed tree: an internal node
		// always has >= 2 children. Defensive guard against a panic below
		// if this invariant is ever violated.
		store.Unlock(parentID)
		return false, fmt.Errorf("btree: internal invariant violated: parent node %d has only one child, cannot rebalance", parentID)
	}

	if !store.TryLock(leafID) {
		store.Unlock(parentID)
		return false, errRestartFromRoot
	}
	_, leaf, _, err := store.ReadNode(leafID)
	if err != nil {
		store.Unlock(leafID)
		store.Unlock(parentID)
		return false, err
	}
	if len(leaf.Keys) > 0 {
		// Concurrently refilled or already repaired: nothing left to do.
		store.Unlock(leafID)
		store.Unlock(parentID)
		return false, nil
	}
	emptyNextLeaf := leaf.NextLeaf

	haveLeftCandidate := j > 0
	haveRightCandidate := j < len(parent.Children)-1

	if haveLeftCandidate {
		leftID := parent.Children[j-1]
		if !store.TryLock(leftID) {
			store.Unlock(leafID)
			store.Unlock(parentID)
			return false, errRestartFromRoot
		}
		leftIsLeaf, left, _, err := store.ReadNode(leftID)
		if err != nil {
			store.Unlock(leftID)
			store.Unlock(leafID)
			store.Unlock(parentID)
			return false, err
		}
		if leftIsLeaf {
			// 2a.4.5 fix: both the borrow-from-left and merge-into-left
			// branches below unconditionally overwrite leftID's NextLeaf
			// field to point directly at leafID (or splice past it). That
			// is only safe if leftID's CURRENT, freshly-read NextLeaf
			// still actually equals leafID -- i.e. leftID and leafID are
			// still true, immediate chain neighbors. If a concurrent
			// Insert has split leftID (writing a new right-half node in
			// between, referenced by leftID.NextLeaf) but has not yet run
			// propagate to link that node into parentID's Children
			// (propagate needs parentID's latch, held here for the whole
			// borrow/merge decision, so it is necessarily still pending),
			// then blindly overwriting leftID.NextLeaf would skip over --
			// and permanently orphan -- that not-yet-linked, live node.
			// Detect this race up front and retry via a fresh findParent,
			// by which point the pending split's propagate call will
			// typically have completed and leftID will no longer be the
			// correct left candidate for leafID (the newly-linked node
			// will be).
			if left.NextLeaf != leafID {
				// Bugfix (subtask 4.5.1.5, issue #38): this branch previously
				// called store.Unlock(leftID) twice and never unlocked leafID
				// at all -- a copy-paste error from the 2a.4.5 orphan-guard
				// fix (commit b31328f) that shipped alongside this exact race
				// check. In production this permanently leaked leafID's latch
				// (any future Lock(leafID) call -- e.g. this very leaf's next
				// repair attempt, or an unrelated concurrent Insert/Delete
				// routed to it -- would block forever) and double-unlocked
				// leftID's mutex, which panics ("sync: unlock of unlocked
				// mutex", or NodeStore.Unlock's own "no outstanding
				// Lock/TryLock" panic if leftID's latch entry had already been
				// evicted in between). Found while writing
				// TestRepairEmptyLeafOrphanRegression (delete_test.go), which
				// deterministically forces this exact branch and reproduces
				// the panic/deadlock against the pre-fix code. Fixed to
				// unlock each of the three distinct latches held here
				// (leftID, leafID, parentID) exactly once, matching every
				// other return path in this function.
				store.Unlock(leftID)
				store.Unlock(leafID)
				store.Unlock(parentID)
				return true, nil
			}
			if len(left.Keys) > 1 {
				// Borrow one key from the left sibling.
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
				newParent := InternalNode{Keys: newParentKeys, Children: append([]uint64(nil), parent.Children...), NextSibling: parent.NextSibling, LowKey: parent.LowKey}

				if err := writeLeaf(store, leftID, newLeft); err != nil {
					store.Unlock(leftID)
					store.Unlock(leafID)
					store.Unlock(parentID)
					return false, err
				}
				if err := writeLeaf(store, leafID, newCurrent); err != nil {
					store.Unlock(leftID)
					store.Unlock(leafID)
					store.Unlock(parentID)
					return false, err
				}
				if err := writeInternal(store, parentID, newParent); err != nil {
					store.Unlock(leftID)
					store.Unlock(leafID)
					store.Unlock(parentID)
					return false, err
				}
				store.Unlock(leftID)
				store.Unlock(leafID)
				store.Unlock(parentID)
				return false, nil
			}

			// Merge current (empty) leaf into the left sibling. Safe to
			// splice leftID straight to emptyNextLeaf here: the
			// left.NextLeaf == leafID race guard above already ruled out
			// the concurrent-split case (see its comment for the full
			// writeup).
			mergedLeft := LeafNode{Keys: left.Keys, FileIDs: left.FileIDs, NextLeaf: emptyNextLeaf}
			if err := writeLeaf(store, leftID, mergedLeft); err != nil {
				store.Unlock(leftID)
				store.Unlock(leafID)
				store.Unlock(parentID)
				return false, err
			}
			store.Unlock(leftID)
			store.Unlock(leafID)
			// leafID abandoned; see the free Delete function's doc comment
			// on "Abandoned node IDs". parentID is still held here and is
			// handed off to finishParentShrinkAfterDelete, which unlocks it.
			return false, t.finishParentShrinkAfterDelete(parentID, parent, j, path)
		}
		store.Unlock(leftID)
	}

	if haveRightCandidate {
		rightID := parent.Children[j+1]
		if !store.TryLock(rightID) {
			store.Unlock(leafID)
			store.Unlock(parentID)
			return false, errRestartFromRoot
		}
		rightIsLeaf, right, _, err := store.ReadNode(rightID)
		if err != nil {
			store.Unlock(rightID)
			store.Unlock(leafID)
			store.Unlock(parentID)
			return false, err
		}
		if rightIsLeaf {
			if len(right.Keys) > 1 {
				// Borrow one key from the right sibling.
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
				newParent := InternalNode{Keys: newParentKeys, Children: append([]uint64(nil), parent.Children...), NextSibling: parent.NextSibling, LowKey: parent.LowKey}

				if err := writeLeaf(store, rightID, newRight); err != nil {
					store.Unlock(rightID)
					store.Unlock(leafID)
					store.Unlock(parentID)
					return false, err
				}
				if err := writeLeaf(store, leafID, newCurrent); err != nil {
					store.Unlock(rightID)
					store.Unlock(leafID)
					store.Unlock(parentID)
					return false, err
				}
				if err := writeInternal(store, parentID, newParent); err != nil {
					store.Unlock(rightID)
					store.Unlock(leafID)
					store.Unlock(parentID)
					return false, err
				}
				store.Unlock(rightID)
				store.Unlock(leafID)
				store.Unlock(parentID)
				return false, nil
			}

			// Merge the right sibling into the current (empty) leaf's slot,
			// keeping leafID's own node ID (mirrors the single-threaded
			// repairEmptyLeaf's right-merge branch).
			mergedCurrent := LeafNode{Keys: right.Keys, FileIDs: right.FileIDs, NextLeaf: right.NextLeaf}
			if err := writeLeaf(store, leafID, mergedCurrent); err != nil {
				store.Unlock(rightID)
				store.Unlock(leafID)
				store.Unlock(parentID)
				return false, err
			}
			store.Unlock(rightID)
			store.Unlock(leafID)
			// rightID abandoned; see the free Delete function's doc comment
			// on "Abandoned node IDs". parentID is still held here and is
			// handed off to finishParentShrinkAfterDelete, which unlocks it.
			return false, t.finishParentShrinkAfterDelete(parentID, parent, j+1, path)
		}
		store.Unlock(rightID)
	}

	// Neither same-parent sibling usable (same-type, i.e. leaf) as a
	// borrow/merge candidate -- mirrors the single-threaded repairEmptyLeaf's
	// final fallback: accept the underflow, leaving the already-written
	// empty leaf in place (still reachable, still correctly linked via
	// NextLeaf).
	store.Unlock(leafID)
	store.Unlock(parentID)
	return false, nil
}

// finishParentShrinkAfterDelete removes the child pointer at removedChildIdx
// (an abandoned node ID a leaf-level merge just spliced out) and its
// associated separator key (at removedChildIdx-1) from parentID's
// already-locked, already-fresh-read InternalNode parent, then rewrites it
// and releases parentID's latch on every path. Mirrors the single-threaded
// shrinkParentAfterMerge's policy exactly (the borrow-or-merge decision is
// not revisited here -- only concurrency-safe locking is added), while fixing
// a latent bug present in that function: NextSibling/LowKey must be
// preserved on every reconstructed InternalNode, since a concurrent crabbing
// walk's move-right recovery (and this package's structural-invariant
// checks) depend on those fields staying correct -- see this section's doc
// comment above.
func (t *Tree) finishParentShrinkAfterDelete(parentID uint64, parent InternalNode, removedChildIdx int, path string) error {
	store := t.Store

	newKeys := make([]string, 0, len(parent.Keys)-1)
	newKeys = append(newKeys, parent.Keys[:removedChildIdx-1]...)
	newKeys = append(newKeys, parent.Keys[removedChildIdx:]...)

	newChildren := make([]uint64, 0, len(parent.Children)-1)
	newChildren = append(newChildren, parent.Children[:removedChildIdx]...)
	newChildren = append(newChildren, parent.Children[removedChildIdx+1:]...)

	newParent := InternalNode{Keys: newKeys, Children: newChildren, NextSibling: parent.NextSibling, LowKey: parent.LowKey}

	if err := writeInternal(store, parentID, newParent); err != nil {
		store.Unlock(parentID)
		return err
	}

	if len(newParent.Children) > 1 {
		// Tombstone policy: parentID still has >= 1 key. Done.
		store.Unlock(parentID)
		return nil
	}

	// parentID degenerated to 0 keys/1 child: splice it out of ITS parent
	// (the grandparent), or collapse the root if parentID IS the root.
	survivingChild := newParent.Children[0]
	ancestorNextSibling := newParent.NextSibling

	// Lock-ordering note: this acquires rootMu while still holding
	// parentID's latch -- the REVERSE of insert.go's documented
	// rootMu-first ordering. Safe for the same reason noted in
	// deleteFromLeafAndRepair above: rootMu is always a wait-for-graph SINK
	// here (its critical sections only ever read/write t.root, never block
	// on acquiring a node latch), so no lock-ordering cycle can form no
	// matter which order a given call site takes rootMu vs. a node latch in.
	t.rootMu.Lock()
	if t.root == parentID {
		t.root = survivingChild
		t.rootMu.Unlock()
		store.Unlock(parentID)
		return nil
	}
	t.rootMu.Unlock()
	store.Unlock(parentID)

	return t.spliceOutDegenerateAncestor(parentID, ancestorNextSibling, survivingChild, path)
}

// spliceOutDegenerateAncestor splices ancestorID (already rewritten as a
// 0-key/1-child InternalNode by the caller, already unlocked) out of its
// current direct parent (the "grandparent"), redirecting the grandparent's
// child pointer straight to ancestorID's single surviving child. This
// mirrors the single-threaded shrinkParentAfterMerge's grandparent-splice
// branch exactly (policy unchanged), adding only concurrency-safe locking:
// a single grandparent latch at a time, located fresh via findParent exactly
// like propagate's ancestor relocation -- this never needs more than a
// parent+child latch budget, matching insert's window-of-2.
//
// ancestorID's level-order NextSibling chain must also be kept connected: some
// node currently has NextSibling == ancestorID, and once ancestorID is spliced
// out (its own node ID abandoned forever, see the free Delete function's doc
// comment on "Abandoned node IDs"), that pointer would otherwise dangle,
// referencing an unreachable node forever (fix round 1 of this subtask
// shipped with exactly that bug: it only patched the common case where the
// true left neighbor happens to be a child of the SAME grandparent
// (grandParent.Children[gj-1]); when ancestorID is instead its grandparent's
// FIRST child, the true neighbor lives one or more levels up and one subtree
// to the left, under a different grandparent entirely, and was never located
// or patched -- see .cdr/runs/2026-07-06/005-verification/verification.json
// for the full writeup of why this is a genuine correctness bug, not just a
// cosmetic invariant violation: crabDeleteOnce and crabInsertOnce/findParent
// all actively dereference NextSibling as a live "move right on overshoot"
// mechanism during concurrent descent, so a dangling pointer can misroute a
// concurrent operation into the abandoned node).
//
// findLeftNeighborAtSameLevel (below) now finds the true left neighbor
// uniformly, whether it is a same-grandparent sibling or lives under an
// entirely different ancestor subtree, using the same level-order
// NextSibling-chain-walk discipline already established by findParent's own
// leaf-level NextLeaf-chain-walk recovery: no special-casing on grandparent
// adjacency is needed.
func (t *Tree) spliceOutDegenerateAncestor(ancestorID, ancestorNextSibling, survivingChild uint64, path string) error {
	store := t.Store

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			crabRetryBackoff(attempt)
		}

		t.rootMu.Lock()
		currentRoot := t.root
		t.rootMu.Unlock()

		grandParentID, err := t.findParent(currentRoot, path, ancestorID)
		if err != nil {
			return err
		}

		store.Lock(grandParentID)
		isLeaf, _, grandParent, err := store.ReadNode(grandParentID)
		if err != nil {
			store.Unlock(grandParentID)
			return err
		}
		if isLeaf {
			store.Unlock(grandParentID)
			return fmt.Errorf("btree: internal invariant violated: ancestor node %d decoded as a leaf", grandParentID)
		}

		gj := indexOfChild(grandParent.Children, ancestorID)
		if gj < 0 {
			// Race: ancestorID's parentage changed concurrently. Retry:
			// findParent will relocate the current grandparent fresh.
			store.Unlock(grandParentID)
			continue
		}

		newChildren := append([]uint64(nil), grandParent.Children...)
		newChildren[gj] = survivingChild
		newGrandParent := InternalNode{
			Keys:        append([]string(nil), grandParent.Keys...),
			Children:    newChildren,
			NextSibling: grandParent.NextSibling,
			LowKey:      grandParent.LowKey,
		}
		if err := writeInternal(store, grandParentID, newGrandParent); err != nil {
			store.Unlock(grandParentID)
			return err
		}

		// Best-effort NextSibling chain fix-up: locate whichever node
		// currently has NextSibling == ancestorID -- regardless of whether
		// it is a same-grandparent sibling (gj > 0) or lives under an
		// entirely different ancestor subtree (gj == 0) -- and repoint it
		// past ancestorID. A TryLock miss (or any other reason the neighbor
		// can't be confirmed/patched) is deliberately NOT treated as
		// errRestartFromRoot: the fix-up is hygiene, not required for key
		// presence/absence correctness, so we simply skip it this round
		// rather than paying for a full restart.
		neighborID, nerr := t.findLeftNeighborAtSameLevel(currentRoot, grandParentID, grandParent, gj, path)
		if nerr != nil {
			store.Unlock(grandParentID)
			return nerr
		}
		if neighborID != noSibling {
			if store.TryLock(neighborID) {
				leftIsLeaf, _, leftInternal, lerr := store.ReadNode(neighborID)
				if lerr == nil && !leftIsLeaf && leftInternal.NextSibling == ancestorID {
					fixed := leftInternal
					fixed.NextSibling = ancestorNextSibling
					if werr := writeInternal(store, neighborID, fixed); werr != nil {
						store.Unlock(neighborID)
						store.Unlock(grandParentID)
						return werr
					}
				}
				store.Unlock(neighborID)
			}
		}

		store.Unlock(grandParentID)
		return nil
	}
}

// findLeftNeighborAtSameLevel locates the node whose NextSibling chain link
// currently points at ancestorID -- i.e. ancestorID's true predecessor at its
// own tree level -- regardless of whether that predecessor happens to be a
// child of ancestorID's immediate parent (the common case) or lives under an
// entirely different ancestor subtree several levels up and one subtree to
// the left (the case that fix round 1 of this subtask missed).
//
// It takes ancestorID's parent (grandParentID/grandParent, already read by
// the caller) and ancestorID's index within it (gj) directly, rather than
// re-deriving them via findParent(ancestorID): by the time this is called,
// the caller has ALREADY rewritten grandParent's Children to splice
// ancestorID out (grandParent.Children[gj] == survivingChild now), so
// ancestorID is no longer reachable as anyone's child at all -- a fresh
// findParent(ancestorID) call here would either error out or, worse, mis-walk
// down some unrelated path following stale key-range routing. Passing the
// pre-computed (grandParentID, grandParent, gj) sidesteps that entirely: the
// walk only ever needs Children[gj-1] (unaffected by the splice, which only
// touched index gj) or higher ancestors (still perfectly findable, since only
// ancestorID's own link was removed).
//
// From there it walks UP toward the root, one parent-hop at a time (via
// findParent for every level above the first, so it always sees the CURRENT
// parent, tolerating concurrent restructuring), until it finds the first
// ancestor that is NOT its own parent's first child. That parent's
// immediately preceding child, call it left, is guaranteed to be the
// ancestor -- at left's own level -- of ancestorID's true predecessor: on the
// walk up, every level in between was entered via "current node is index 0 of
// its parent," so descending back down from left by always taking the LAST
// child exactly as many times as levels were walked up lands exactly on
// ancestorID's true predecessor.
//
// If the walk up reaches the root without ever finding an ancestor that
// isn't a first child, ancestorID (and its whole leftmost spine) has no
// predecessor at all -- ancestorID's own LowKey == "" transitively. The
// function then returns noSibling and a nil error, signalling "nothing to
// patch."
//
// Read-only and best-effort beyond the first hop: never holds more than one
// latch at a time (each hop locks, reads, and unlocks before the next; the
// first hop reuses grandParent's already-locked-by-caller data without
// re-locking it), so it adds no deadlock surface -- the caller separately
// (Try)Locks the returned candidate itself and re-verifies its NextSibling
// before trusting it, exactly as fix round 1 already did for the
// same-grandparent case. Any race that makes the walk's bookkeeping stale (a
// concurrent split/merge shifting levels mid-walk) can only ever cause this
// function to return noSibling or a node whose NextSibling turns out not to
// equal ancestorID on the caller's final check -- never an incorrect patch of
// the wrong node -- since the caller always re-verifies before writing.
func (t *Tree) findLeftNeighborAtSameLevel(currentRoot, grandParentID uint64, grandParent InternalNode, gj int, path string) (uint64, error) {
	store := t.Store

	levelsUp := 0
	curParentID := grandParentID
	curChildren := grandParent.Children
	curIdx := gj
	for {
		if curIdx < 0 {
			// Race: caller's gj no longer lines up (shouldn't happen for the
			// first iteration, but a defensive guard for consistency with
			// the up-walk's own idx<0 handling below).
			return noSibling, nil
		}

		if curIdx > 0 {
			left := curChildren[curIdx-1]

			// Descend from left via "always take the last child" exactly
			// levelsUp times to reach ancestorID's own level.
			neighbor := left
			for i := 0; i < levelsUp; i++ {
				store.Lock(neighbor)
				nIsLeaf, _, nInternal, err := store.ReadNode(neighbor)
				if err != nil {
					store.Unlock(neighbor)
					return noSibling, err
				}
				if nIsLeaf || len(nInternal.Children) == 0 {
					// Concurrent restructuring made the computed depth no
					// longer line up. Abandon the fix-up this round --
					// purely hygiene, never correctness-required.
					store.Unlock(neighbor)
					return noSibling, nil
				}
				next := nInternal.Children[len(nInternal.Children)-1]
				store.Unlock(neighbor)
				neighbor = next
			}
			return neighbor, nil
		}

		if curParentID == currentRoot {
			// Reached the root without ever finding a non-first-child
			// ancestor: no predecessor exists anywhere above ancestorID.
			return noSibling, nil
		}

		upParentID, err := t.findParent(currentRoot, path, curParentID)
		if err != nil {
			return noSibling, err
		}

		store.Lock(upParentID)
		isLeaf, _, upParent, err := store.ReadNode(upParentID)
		if err != nil {
			store.Unlock(upParentID)
			return noSibling, err
		}
		if isLeaf {
			store.Unlock(upParentID)
			return noSibling, fmt.Errorf("btree: internal invariant violated: ancestor node %d decoded as a leaf", upParentID)
		}
		idx2 := indexOfChild(upParent.Children, curParentID)
		store.Unlock(upParentID)
		if idx2 < 0 {
			// Race: curParentID's own parentage changed since findParent
			// located upParentID. Hygiene-only fix-up, so give up this
			// round rather than paying for a full restart.
			return noSibling, nil
		}

		curParentID = upParentID
		curChildren = upParent.Children
		curIdx = idx2
		levelsUp++
	}
}
