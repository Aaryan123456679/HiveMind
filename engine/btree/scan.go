package btree

import (
	"fmt"
	"sort"
	"strings"
)

// ScanEntry is one (path, fileID) pair returned by PrefixScan, in ascending
// sorted key order (the same order LeafNode.Keys is required to be kept in
// by the insert/delete logic in insert.go/delete.go).
type ScanEntry struct {
	Path   string
	FileID uint64
}

// PrefixScan returns every (path, fileID) entry in the B+Tree rooted at
// rootNodeID whose path has prefix as a string prefix, in ascending sorted
// order. It returns a nil (empty) slice and a nil error if no keys share the
// prefix -- that is a normal, expected outcome, mirroring Lookup's
// not-found=nil-error convention, not an error condition.
//
// Like Lookup, PrefixScan does not special-case rootNodeID == reservedNodeID
// (a tree that has never had anything inserted into it): that case is out of
// scope here too, per lookup.go's Lookup doc comment, and behaves the same
// way Lookup does (an error surfaces from the underlying ReadNode call).
//
// Algorithm: this is the standard B+Tree range-scan built on top of leaf
// sibling pointers (LeafNode.NextLeaf, see node.go). It descends exactly as
// Lookup would for the key "prefix" itself (via the same shared
// descendToLeaf helper used by Lookup and Insert), landing on the one leaf
// that could contain the first key sharing the prefix. From there it scans
// forward within that leaf, and follows NextLeaf across sibling leaves as
// needed, collecting every key that has prefix as a string prefix. Because
// LeafNode.Keys is kept in sorted ascending order tree-wide (an invariant
// maintained by Insert/Delete), all keys sharing a given prefix are
// necessarily contiguous in this scan order: as soon as a visited key no
// longer has the prefix, every subsequent key (being >= it, lexicographically)
// cannot have the prefix either, so the scan can stop immediately (early
// exit) instead of walking the rest of the tree.
func PrefixScan(store *NodeStore, rootNodeID uint64, prefix string) ([]ScanEntry, error) {
	_, leaf, err := descendToLeaf(store, rootNodeID, prefix)
	if err != nil {
		return nil, err
	}

	var results []ScanEntry
	// sort.SearchStrings returns the first index i such that
	// leaf.Keys[i] >= prefix, i.e. the first position a key sharing the
	// prefix (or the exact prefix itself, if it is also a stored key) could
	// appear at.
	i := sort.SearchStrings(leaf.Keys, prefix)

	for {
		for ; i < len(leaf.Keys); i++ {
			if !strings.HasPrefix(leaf.Keys[i], prefix) {
				return results, nil
			}
			results = append(results, ScanEntry{Path: leaf.Keys[i], FileID: leaf.FileIDs[i]})
		}

		if leaf.NextLeaf == noSibling {
			return results, nil
		}

		isLeaf, nextLeaf, _, err := store.ReadNode(leaf.NextLeaf)
		if err != nil {
			return nil, err
		}
		if !isLeaf {
			return nil, fmt.Errorf("btree: internal invariant violated: NextLeaf pointer %d decoded as an internal node", leaf.NextLeaf)
		}
		leaf = nextLeaf
		i = 0
	}
}
