package btree

import (
	"sync"
	"sync/atomic"
)

// nodeLatch is the in-memory concurrency-control state associated with exactly one
// node ID: a writer-only mutex (used for latch-crabbing by the insert/delete
// algorithms implemented in subtasks 2a.4.2/2a.4.3) paired with an atomic version
// counter (used for optimistic, lock-free reads implemented in subtask 2a.4.4).
//
// Node identity in this package is a uint64 node ID, not an in-memory object: nodes
// are decoded from disk into fresh LeafNode/InternalNode value structs on every
// ReadNode call (see lookup.go) rather than being cached as live, shared objects.
// Consequently "every node carries a latch and a version counter" (2a.4.1's
// acceptance criterion) is satisfied at the NodeStore level: NodeStore owns a
// registry of nodeLatch values keyed by node ID (see NodeStore.latches below), lazily
// created on first access, which is the only stable place shared concurrent
// goroutines can rendezvous on a given node's concurrency-control state.
//
// Locking/versioning protocol (deliberately decided now so 2a.4.2-2a.4.5 can build
// directly on it without re-litigating the design):
//
//   - mu is a plain sync.Mutex, not a sync.RWMutex. Readers (2a.4.4) never take it at
//     all -- "a reader never blocks a writer or another reader" (2a.4.4's acceptance
//     criterion) requires the read fast path to be fully lock-free. mu is acquired
//     only by writers performing latch-crabbing: lock parent, lock child, release
//     parent (2a.4.2/2a.4.3).
//   - version uses a single-increment-after-mutation scheme, NOT a seqlock-style
//     pre/post (odd = in-progress) pair. A writer holds mu for the entire duration of
//     a structural mutation (read-modify-write), and bumps version by exactly one
//     only after the mutated content has been durably written (see WriteNode).
//     Readers must take a "before" version, read/copy out the node's contents in
//     full, then take an "after" version and retry the whole read if the two differ.
//     Because the version bump happens strictly after the writer's content mutation
//     is complete (never before, never mid-mutation), a reader that observes the same
//     version before and after is guaranteed the content it copied was never
//     concurrently mutated; a reader does not need to distinguish "even/in-progress"
//     from "odd/stable" the way a full seqlock would, because there is no window
//     where a partially-written node is visible under an unchanged version number.
//   - WriteNode (lookup.go) is the sole choke point through which every structural
//     mutation to a node's on-disk content currently flows (both insert.go and
//     delete.go funnel all their node writes through it). WriteNode bumps the
//     target node's version counter by exactly one immediately after its durable
//     write succeeds. WriteNode deliberately does NOT itself acquire the node's
//     latch: 2a.4.2/2a.4.3's crabbing algorithms need to hold a child's latch across
//     a read-decide-write sequence that may span multiple internal calls, and a plain
//     sync.Mutex is not reentrant, so re-locking inside WriteNode would deadlock a
//     caller that already holds it. The required convention going forward (binding on
//     2a.4.2+): any call site that mutates a node concurrently with other goroutines
//     MUST call Lock(nodeID) before, and Unlock(nodeID) after, its WriteNode call(s)
//     for that node. This subtask's own existing call sites (insert.go, delete.go)
//     are single-threaded today and are NOT updated to take the latch yet, per this
//     subtask's scope (that wiring is 2a.4.2/2a.4.3's job); this subtask only adds
//     the fields, the registry, and the version-bump-on-write behavior.
type nodeLatch struct {
	mu      sync.Mutex
	version atomic.Uint64
}

// latchFor returns the nodeLatch associated with nodeID, lazily creating it on first
// access. Safe for concurrent use; the same *nodeLatch is always returned for a given
// nodeID for the lifetime of the NodeStore.
func (s *NodeStore) latchFor(nodeID uint64) *nodeLatch {
	s.latchesMu.Lock()
	defer s.latchesMu.Unlock()

	if s.latches == nil {
		s.latches = make(map[uint64]*nodeLatch)
	}
	l, ok := s.latches[nodeID]
	if !ok {
		l = &nodeLatch{}
		s.latches[nodeID] = l
	}
	return l
}

// Lock acquires nodeID's write latch. Writers performing latch-crabbing (2a.4.2's
// insert, 2a.4.3's delete) must hold a node's latch for the entire duration they
// have that node's content "checked out" for a structural mutation, and must call
// Lock on a child before releasing the lock on its parent (crab, don't leapfrog).
// Readers must never call Lock; the optimistic read path (2a.4.4) relies on this to
// guarantee a reader never blocks a writer or another reader.
func (s *NodeStore) Lock(nodeID uint64) {
	s.latchFor(nodeID).mu.Lock()
}

// Unlock releases nodeID's write latch previously acquired via Lock. Unlock must not
// be called on a nodeID whose latch is not currently held by the caller.
func (s *NodeStore) Unlock(nodeID uint64) {
	s.latchFor(nodeID).mu.Unlock()
}

// TryLock attempts to acquire nodeID's write latch without blocking, reporting
// whether it succeeded. This exists specifically for the hand-over-hand
// ("lock next, then release current") steps of crabInsert/findParent
// (insert.go): those steps must never block while still holding a *different*
// node's latch, because doing so is exactly what makes a lock-ordering cycle
// between concurrent crabbing walks possible in the first place (GitHub issue
// #9's deadlock finding). Callers that get false back must release every
// latch they currently hold and restart their walk from the root rather than
// waiting -- see errRestartFromRoot in insert.go.
func (s *NodeStore) TryLock(nodeID uint64) bool {
	return s.latchFor(nodeID).mu.TryLock()
}

// Version returns nodeID's current version counter. Callers on the optimistic read
// path (2a.4.4) call this once before reading a node's contents and once after,
// retrying the read if the two values differ -- see the protocol documented on
// nodeLatch above. Version never blocks: it is a single atomic load.
func (s *NodeStore) Version(nodeID uint64) uint64 {
	return s.latchFor(nodeID).version.Load()
}

// restartFromRootCount is a package-level, purely observational counter
// incremented once every time crabInsert (insert.go), crabDelete (delete.go),
// or Tree.Lookup (lookup.go) restarts its walk from the tree's root --
// whether because a hand-over-hand TryLock miss forced a crabbing writer to
// give up and restart (errRestartFromRoot), or because an optimistic reader
// observed a concurrent structural mutation mid-read (errOptimisticRetry).
//
// This is pending.md's "consider an optional attempt counter/metric (not a
// hard cap) to make pathological retry storms observable" recommendation
// (surfaced during task-2a.4.2 verification): none of these restart loops
// have, or should have, a maximum-attempt cap -- giving up would mean
// silently dropping a write or a read, which this package never does. This
// counter changes nothing about that: it is purely additive instrumentation,
// read with a single atomic load/increment, with no effect on retry timing,
// backoff, or control flow. It exists solely so an operator (or a test) can
// notice an abnormally high restart rate, which would otherwise be invisible.
var restartFromRootCount atomic.Uint64

// RestartFromRootCount returns the total number of times, across every Tree
// in this process, that a crabInsert/crabDelete/Tree.Lookup call has
// restarted its walk from the tree's root since process start. See
// restartFromRootCount's doc comment for what this counts and why. Intended
// for observability/metrics and tests; never consulted by any retry logic.
func RestartFromRootCount() uint64 {
	return restartFromRootCount.Load()
}
