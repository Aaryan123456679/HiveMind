package btree

import (
	"fmt"
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
//
// Update (subtask 4.5.1.3): NodeStore.latches is now bounded, not an
// ever-growing map keyed by every distinct node ID ever touched -- see
// acquireLatch/releaseLatch below and their doc comments for the reference-
// counted eviction policy this adds, and in particular the "why version==0
// is a load-bearing gate, not an optimization" reasoning, which is the part
// of this change that actually matters for correctness.
type nodeLatch struct {
	mu      sync.Mutex
	version atomic.Uint64

	// refs is the number of NodeStore-level calls (acquireLatch/releaseLatch
	// pairs -- see below) currently holding an outstanding reference to this
	// object, i.e. that have called acquireLatch but not yet called the
	// matching releaseLatch. It is guarded entirely by NodeStore.latchesMu,
	// never by mu and never accessed atomically: every read or write of refs
	// happens inside a latchesMu critical section, by construction (see
	// acquireLatch/releaseLatch).
	refs int
}

// latchFor returns the nodeLatch associated with nodeID, lazily creating it on first
// access, WITHOUT taking part in the refcounted eviction scheme below (it never
// touches refs). Safe for concurrent use.
//
// latchFor is intentionally left as the only accessor WriteNode (lookup.go) uses.
// By this file's binding convention, any concurrent WriteNode(nodeID, ...) call
// must already be wrapped in a Lock(nodeID)/Unlock(nodeID) pair, whose own
// acquireLatch call (see Lock below) already holds an outstanding reference that
// pins this nodeID's entry for the whole call -- so latchFor is guaranteed to find
// (never evict out from under, never need to create outside of) the exact object
// Lock is holding locked. Several existing tests (node_test.go, lookup_test.go)
// also call WriteNode directly without Lock, from single-threaded setup code;
// routing WriteNode through acquireLatch instead would leave those call sites'
// increments permanently unbalanced (no corresponding releaseLatch call), so
// WriteNode deliberately keeps using this simpler, non-refcounted accessor.
func (s *NodeStore) latchFor(nodeID uint64) *nodeLatch {
	s.latchesMu.Lock()
	defer s.latchesMu.Unlock()
	return s.getOrCreateLocked(nodeID)
}

// getOrCreateLocked returns the nodeLatch for nodeID, creating it if absent.
// Callers MUST already hold s.latchesMu.
func (s *NodeStore) getOrCreateLocked(nodeID uint64) *nodeLatch {
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

// acquireLatch returns the nodeLatch for nodeID (creating it if necessary) and
// increments its refs count by one, atomically with the lookup/creation under
// latchesMu. Every call MUST be paired with exactly one later call to
// releaseLatch(nodeID, l) once the caller is completely done using the returned
// pointer -- see Lock/Unlock/TryLock/Version below for the exact pairing in each
// case, and releaseLatch's doc comment for why holding this reference open is
// what makes eviction race-free (it is the whole mechanism that prevents two
// goroutines from ever ending up with two different *nodeLatch objects for the
// same node ID).
func (s *NodeStore) acquireLatch(nodeID uint64) *nodeLatch {
	s.latchesMu.Lock()
	defer s.latchesMu.Unlock()
	l := s.getOrCreateLocked(nodeID)
	l.refs++
	return l
}

// peekLatch returns the nodeLatch currently registered for nodeID, if any,
// WITHOUT creating one and WITHOUT touching refs. It is used only by Unlock,
// where the caller's own prior Lock/TryLock call already holds an outstanding
// acquireLatch reference on nodeID that has not yet been released -- so this
// lookup is guaranteed to find that exact same object; ok is false only if the
// caller violated the Lock/Unlock pairing contract (calling Unlock without a
// matching outstanding Lock/TryLock).
func (s *NodeStore) peekLatch(nodeID uint64) (l *nodeLatch, ok bool) {
	s.latchesMu.Lock()
	defer s.latchesMu.Unlock()
	l, ok = s.latches[nodeID]
	return l, ok
}

// releaseLatch balances a prior acquireLatch(nodeID) call for the SAME l it
// returned: it decrements l.refs and, only if that drops refs to exactly zero
// AND l.version.Load() == 0, removes nodeID's entry from the registry --
// bounding NodeStore.latches' size to (roughly) the number of distinct node IDs
// that have ever actually been mutated (via WriteNode) plus whatever is
// currently in flight, rather than every distinct node ID ever passed to
// Lock/TryLock/Version across the process's entire lifetime. This is the
// concrete fix for this subtask's acceptance criterion.
//
// Why version==0 is a load-bearing gate, not an optimization: Tree.Lookup's
// optimistic read path (readNodeOptimistic in lookup.go) takes a "before"
// version, reads the node, then takes an "after" version, and trusts the read
// only if the two match. If evicting a nodeLatch also discarded its version
// counter, a later Lock/Version call recreating nodeID's entry would start back
// at version 0 -- indistinguishable from "no mutation happened" even though the
// node may have accumulated many real mutations before its entry was evicted.
// Concretely: a node at version N gets evicted while idle; a reader's "before"
// read finds no entry, creates a fresh one, and observes 0; a writer's entire
// Lock-WriteNode-Unlock cycle lands inside the reader's read window and itself
// drops refs back to zero on Unlock, evicting again; the reader's "after" read
// then also finds no entry and also observes a fresh 0. The reader would see
// 0 == 0 and wrongly conclude nothing changed, despite a real WriteNode call
// having happened in between -- a silent lost-update bug in the optimistic read
// protocol, not merely a memory-bloat one. Gating eviction on version==0 makes
// this impossible: once a node's version has ever left 0, its entry (and
// therefore its version history) is never removed from the registry for the
// life of the process, exactly matching this file's pre-4.5.1.3 behavior for
// that subset of node IDs. Only node IDs that have been locked/probed but never
// actually written -- the dominant source of registry growth in practice
// (hand-over-hand crabbing hops that release without mutating, TryLock misses,
// and Tree.Lookup's Version probes) -- are ever reclaimed.
func (s *NodeStore) releaseLatch(nodeID uint64, l *nodeLatch) {
	s.latchesMu.Lock()
	defer s.latchesMu.Unlock()

	l.refs--
	if l.refs == 0 && l.version.Load() == 0 {
		// Defense in depth: only delete if the registry still points at this
		// exact object. This is always true given the invariant that an entry
		// is never deleted while any acquireLatch reference on it remains
		// outstanding (refs > 0), so nothing could have replaced it out from
		// under a caller that still held a reference -- but checking cur == l
		// rather than blindly deleting by key costs nothing and removes any
		// doubt.
		if cur, ok := s.latches[nodeID]; ok && cur == l {
			delete(s.latches, nodeID)
		}
	}
}

// Lock acquires nodeID's write latch. Writers performing latch-crabbing (2a.4.2's
// insert, 2a.4.3's delete) must hold a node's latch for the entire duration they
// have that node's content "checked out" for a structural mutation, and must call
// Lock on a child before releasing the lock on its parent (crab, don't leapfrog).
// Readers must never call Lock; the optimistic read path (2a.4.4) relies on this to
// guarantee a reader never blocks a writer or another reader.
//
// Lock's acquireLatch reference on nodeID is deliberately kept open (not released)
// across the blocking mu.Lock() call and is only released by the matching Unlock
// call -- this is what pins nodeID's registry entry (preventing eviction) for the
// caller's entire "checked out" window, guaranteeing Unlock's later peekLatch call
// finds the identical object.
func (s *NodeStore) Lock(nodeID uint64) {
	l := s.acquireLatch(nodeID)
	l.mu.Lock()
}

// Unlock releases nodeID's write latch previously acquired via Lock. Unlock must not
// be called on a nodeID whose latch is not currently held by the caller.
//
// Unlock unlocks the mutex FIRST, then releases the acquireLatch reference Lock
// opened (which may evict the entry). This ordering is load-bearing: if the entry
// were evicted (refs dropping to zero) before the mutex was unlocked, a new
// Lock(nodeID) call racing in that gap would create a brand-new nodeLatch with a
// fresh, unlocked mutex and could immediately acquire it -- while the original,
// now-orphaned mutex is still actually held by this call -- giving two goroutines
// simultaneous "ownership" of nodeID's latch, a total mutual-exclusion failure.
// Unlocking first means that by the time the entry becomes eligible for eviction,
// its mutex is already unlocked, so any later Lock call -- whether it reuses this
// object (not yet evicted) or creates a fresh replacement (already evicted) -- is
// acquiring an unlocked mutex either way: behaviorally identical, no lost wakeup,
// no double-lock.
func (s *NodeStore) Unlock(nodeID uint64) {
	l, ok := s.peekLatch(nodeID)
	if !ok {
		panic(fmt.Sprintf("btree: Unlock called for node %d with no outstanding Lock/TryLock", nodeID))
	}
	l.mu.Unlock()
	s.releaseLatch(nodeID, l)
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
//
// On success, TryLock's acquireLatch reference is kept open exactly like Lock's,
// released only by the caller's later Unlock(nodeID) call. On failure, nothing was
// ever locked, so the reference is released immediately (there is no mutex to
// unlock first -- the ordering concern in Unlock's doc comment does not apply
// here, since this path never held the mutex).
func (s *NodeStore) TryLock(nodeID uint64) bool {
	l := s.acquireLatch(nodeID)
	if l.mu.TryLock() {
		return true
	}
	s.releaseLatch(nodeID, l)
	return false
}

// Version returns nodeID's current version counter. Callers on the optimistic read
// path (2a.4.4) call this once before reading a node's contents and once after,
// retrying the read if the two values differ -- see the protocol documented on
// nodeLatch above. Version never blocks on nodeLatch.mu: it only ever takes a
// single atomic load, bracketed by a brief acquireLatch/releaseLatch pair purely
// to participate correctly in the registry's refcounted eviction bookkeeping
// (see releaseLatch's doc comment for why this never causes Version to observe a
// version that has been incorrectly reset by eviction).
func (s *NodeStore) Version(nodeID uint64) uint64 {
	l := s.acquireLatch(nodeID)
	v := l.version.Load()
	s.releaseLatch(nodeID, l)
	return v
}

// latchRegistrySize returns the current number of entries in NodeStore.latches.
// Test-only observability for this subtask's acceptance criterion (the registry
// must stay bounded rather than growing linearly with distinct node IDs ever
// locked) -- see latch_test.go's TestNodeLatchRegistryBounded.
func (s *NodeStore) latchRegistrySize() int {
	s.latchesMu.Lock()
	defer s.latchesMu.Unlock()
	return len(s.latches)
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
// (surfaced during task-2a.4.2 verification).
//
// Update (subtask 4.5.1.2): crabInsert and crabDelete now bound their own
// restart loops at crabMaxRestarts (insert.go), surfacing errTooManyRestarts
// as a purely theoretical, never-observed-in-practice livelock guard rather
// than retrying forever -- see crabMaxRestarts' doc comment for why this is
// not a correctness fix. Tree.Lookup's restart loop (lookup.go) is
// deliberately left uncapped: an optimistic reader that gives up would mean
// silently dropping a read, which this package still never does for reads.
// This counter itself changes nothing about any of that either way: it is
// purely additive instrumentation, read with a single atomic load/increment,
// with no effect on retry timing, backoff, or control flow. It exists solely
// so an operator (or a test) can notice an abnormally high restart rate,
// which would otherwise be invisible.
var restartFromRootCount atomic.Uint64

// RestartFromRootCount returns the total number of times, across every Tree
// in this process, that a crabInsert/crabDelete/Tree.Lookup call has
// restarted its walk from the tree's root since process start. See
// restartFromRootCount's doc comment for what this counts and why. Intended
// for observability/metrics and tests; never consulted by any retry logic.
func RestartFromRootCount() uint64 {
	return restartFromRootCount.Load()
}
