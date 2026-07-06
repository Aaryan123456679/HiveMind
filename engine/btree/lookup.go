package btree

import (
	"errors"
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

// errOptimisticRetry is returned internally by lookupOnce (never by the
// public Tree.Lookup wrapper) the instant a version-mismatch is detected on
// any node visited during descent -- i.e. a writer's structural mutation
// (WriteNode) overlapped this attempt's read of that node. Because the
// optimistic read path holds no latch across hops, a mismatch on any single
// node makes every routing decision already made in this attempt
// (in particular, which move-right hops were taken) untrustworthy: the only
// safe recovery is to discard the whole attempt and restart the descent from
// the tree's current root, mirroring insert.go's errRestartFromRoot
// discipline for TryLock misses (this is the read-side analogue of that same
// "always safe to redo, since nothing has been mutated yet" reasoning).
var errOptimisticRetry = errors.New("btree: optimistic read observed a concurrent structural mutation; restart from root")

// optimisticReadHook, if non-nil, is invoked synchronously by
// readNodeOptimistic immediately after ReadNode returns (content already
// copied into local variables) but before the confirming second Version
// load. This is the one deterministic window in which a test can pause a
// lookup goroutine and let a concurrent writer land a real WriteNode call on
// the exact node just read, forcing a genuine version-mismatch retry rather
// than relying on probabilistic contention. Used only by
// TestOptimisticRead/ForcedRetryDeterministic in lookup_test.go; nil (a
// no-op) in production, exactly mirroring crabRetryHook's shape in
// insert.go.
var optimisticReadHook func(nodeID uint64)

// optimisticRetryHook, if non-nil, is invoked synchronously every time
// Tree.Lookup's descent detects a version mismatch and is about to discard
// the current attempt and restart from the root, with the ID of the node
// whose version changed. Used only by tests to deterministically observe
// that the retry path was actually taken; nil (a no-op) in production.
// Mirrors crabRetryHook's role for the write-side TryLock-miss restart path.
var optimisticRetryHook func(nodeID uint64)

// readNodeOptimistic performs one lock-free, version-bracketed read of
// nodeID: it snapshots the node's version, reads its content via the
// ordinary (unsynchronized) ReadNode, invokes optimisticReadHook (test-only
// pause point), then snapshots the version again. ok is true only if the two
// version snapshots are equal, meaning no WriteNode call for nodeID
// completed its version bump anywhere within this read's window -- see
// lookup.go's package-level doc comment and this subtask's plan.md for the
// full torn-read safety argument (single-syscall, page-sized ReadAt/WriteAt
// calls make the common case safe; a residual, documented, non-blocking risk
// remains on filesystems/platforms that don't page-atomically serialize a
// same-page concurrent read/write, which this package does not attempt to
// close via locking without reintroducing the very reader/writer blocking
// this subtask exists to avoid).
//
// readNodeOptimistic never calls NodeStore.Lock or NodeStore.TryLock -- it
// touches only Version (a non-blocking atomic load) and ReadNode (a plain,
// unsynchronized read). This is the load-bearing invariant for this
// subtask's "readers never block writers, writers never block readers"
// acceptance criterion.
func readNodeOptimistic(store *NodeStore, nodeID uint64) (isLeaf bool, leaf LeafNode, internal InternalNode, ok bool, err error) {
	v1 := store.Version(nodeID)
	isLeaf, leaf, internal, err = store.ReadNode(nodeID)
	if err != nil {
		return false, LeafNode{}, InternalNode{}, false, err
	}
	if optimisticReadHook != nil {
		optimisticReadHook(nodeID)
	}
	v2 := store.Version(nodeID)
	return isLeaf, leaf, internal, v1 == v2, nil
}

// lookupOnce performs a single, lock-free attempt at descending the B+Tree
// rooted at rootID toward path, mirroring crabInsertOnce's (insert.go)
// Blink-tree move-right descent shape exactly, minus all latching: every
// node visited (the current node at each level, plus every sibling peeked
// while chasing NextLeaf/NextSibling looking for an overshoot caused by a
// concurrent split) is read via readNodeOptimistic instead of
// Lock+ReadNode+Unlock. A version mismatch on ANY of those reads aborts the
// entire attempt with errOptimisticRetry rather than retrying just that one
// hop, since this function holds no latch across hops and therefore cannot
// otherwise be sure earlier hops in this same attempt remain valid once a
// later one turns out to have raced a writer.
func lookupOnce(store *NodeStore, rootID uint64, path string) (fileID uint64, found bool, err error) {
	currentID := rootID
	isLeaf, leaf, internal, ok, err := readNodeOptimistic(store, currentID)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		if optimisticRetryHook != nil {
			optimisticRetryHook(currentID)
		}
		return 0, false, errOptimisticRetry
	}

	for !isLeaf {
		// Move-right recovery: peek the current internal node's NextSibling
		// chain, comparing path against each candidate's LowKey (its fixed,
		// true subtree lower bound -- never its own currently-populated max
		// separator, which under-corrects for sparsely/out-of-order-filled
		// nodes; see InternalNode.LowKey's doc comment in node.go and
		// crabInsertOnce's identical logic in insert.go).
		for internal.NextSibling != noSibling {
			nextID := internal.NextSibling
			nextIsLeaf, _, nextInternal, ok, err := readNodeOptimistic(store, nextID)
			if err != nil {
				return 0, false, err
			}
			if !ok {
				if optimisticRetryHook != nil {
					optimisticRetryHook(nextID)
				}
				return 0, false, errOptimisticRetry
			}
			if nextIsLeaf {
				return 0, false, fmt.Errorf("btree: internal invariant violated: NextSibling chain led to a leaf node %d", nextID)
			}
			if nextInternal.LowKey != "" && path < nextInternal.LowKey {
				break
			}
			currentID = nextID
			internal = nextInternal
		}

		i := sort.Search(len(internal.Keys), func(i int) bool { return path < internal.Keys[i] })
		currentID = internal.Children[i]
		isLeaf, leaf, internal, ok, err = readNodeOptimistic(store, currentID)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			if optimisticRetryHook != nil {
				optimisticRetryHook(currentID)
			}
			return 0, false, errOptimisticRetry
		}
	}

	// Move-right recovery at leaf level: peek NextLeaf, comparing path
	// against the sibling's own first key (leaves store real keys directly,
	// so this is exact -- see crabInsertOnce's identical logic).
	for leaf.NextLeaf != noSibling {
		nextID := leaf.NextLeaf
		nextIsLeaf, nextLeaf, _, ok, err := readNodeOptimistic(store, nextID)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			if optimisticRetryHook != nil {
				optimisticRetryHook(nextID)
			}
			return 0, false, errOptimisticRetry
		}
		if !nextIsLeaf {
			return 0, false, fmt.Errorf("btree: internal invariant violated: NextLeaf chain led to a non-leaf node %d", nextID)
		}
		// 2a.4.5 fix: an empty sibling must never be moved into -- see
		// crabInsertOnce's identical fix (insert.go) for the full
		// root-cause writeup. A NextLeaf sibling that is completely empty
		// is, under Delete's tombstone policy, a drained leaf awaiting its
		// own repair, not a genuine split-off right half; it carries no
		// usable lower-bound key of its own, so falling through to "move
		// right" whenever it happens to be empty would misroute this read
		// into an unrelated, out-of-range leaf.
		if len(nextLeaf.Keys) == 0 || path < nextLeaf.Keys[0] {
			break
		}
		currentID = nextID
		leaf = nextLeaf
	}

	i := sort.SearchStrings(leaf.Keys, path)
	if i < len(leaf.Keys) && leaf.Keys[i] == path {
		return leaf.FileIDs[i], true, nil
	}
	return 0, false, nil
}

// Lookup performs a lock-free, optimistic point lookup of path in t,
// concurrency-safe against Tree.Insert/Tree.Delete (2a.4.2/2a.4.3) and other
// concurrent Tree.Lookup calls: it never calls NodeStore.Lock or TryLock, so
// it can never block a writer and can never be blocked by one (this
// subtask's, 2a.4.4's, acceptance criterion). Each node visited is read via
// readNodeOptimistic's version-before/content-read/version-after protocol;
// whenever any node's version changed during the read (a concurrent writer's
// WriteNode call overlapped it), the entire lookup is discarded and retried
// from the tree's current root (t.Root(), re-read on every attempt in case a
// concurrent root split installed a new root), with the same jittered
// backoff (crabRetryBackoff) and "no retry cap" convention insert.go/
// delete.go's TryLock-miss restart loops already use.
//
// This is a NEW, additive entry point alongside the pre-existing, untouched
// free function Lookup (above): Lookup remains the single-threaded, non-
// latching Phase-1 API relied on throughout the engine; Tree.Lookup is the
// concurrency-safe API for callers that also use Tree.Insert/Tree.Delete.
func (t *Tree) Lookup(path string) (fileID uint64, found bool, err error) {
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			crabRetryBackoff(attempt)
		}
		root := t.Root()
		if root == reservedNodeID {
			return 0, false, nil
		}
		fileID, found, err := lookupOnce(t.Store, root, path)
		if err == errOptimisticRetry {
			continue
		}
		return fileID, found, err
	}
}
