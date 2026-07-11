package split

import (
	"sync"
	"sync/atomic"
)

// fileSplitState is the in-memory CAS state associated with exactly one
// file ID: a single atomic boolean recording whether a split is currently
// in progress for that file.
//
// The zero value (inProgress == false) is the correct initial state: no
// file starts out with a split already in progress.
//
// Update (subtask 4.5.3.2): fileSplitState now also carries refs, mirroring
// engine/btree/latch.go's nodeLatch.refs -- see acquireGuard/releaseGuard
// below and FileGuard's doc comment for the reference-counted eviction
// policy this adds.
type fileSplitState struct {
	inProgress atomic.Bool

	// refs is the number of FileGuard-level calls (acquireGuard/releaseGuard
	// pairs -- see below) currently holding an outstanding reference to this
	// object, i.e. that have called acquireGuard but not yet called the
	// matching releaseGuard. It is guarded entirely by FileGuard.mu, never
	// atomically: every read or write of refs happens inside a mu critical
	// section, by construction (see acquireGuard/releaseGuard).
	refs int
}

// FileGuard is a per-file registry of split-in-progress flags, keyed by
// FileID (see split.Signal.FileID). It exists to guarantee that when
// multiple goroutines concurrently observe the same file crossing its
// split threshold -- whether via concurrent appends racing Trigger.Detect
// (2b.1.1), or multiple callers independently deciding to attempt a split
// for the same file -- exactly one of them wins the right to actually
// perform the split; every other caller learns "already in progress" and
// backs off.
//
// FileGuard is deliberately independent of Trigger: Trigger is stateless
// and never remembers which fileIDs it already signaled for (see
// trigger.go's doc comment on why introducing such memory there would risk
// drifting out of sync with the source of truth for a file's size).
// FileGuard is exactly that missing piece of state, but scoped narrowly to
// "is a split in progress for this fileID", not to threshold-crossing
// detection. The two compose naturally -- a caller typically calls
// TryAcquire(signal.FileID) after receiving a Signal from Trigger.Detect --
// but 2b.1.1 and 2b.1.2 do not wire them together directly; that wiring,
// together with the catalog SPLITTING-status transition, is 2b.1.3's job.
//
// Concurrency: FileGuard is safe for concurrent use by multiple goroutines,
// including concurrent TryAcquire/Release/InProgress calls for the same or
// different fileIDs. The registry follows the same lazily-created,
// per-key-atomic-state-via-map idiom established by engine/btree/latch.go's
// NodeStore (a package-level sync.Mutex guards only map access/creation;
// the actual per-key synchronization -- here, a single atomic.Bool's
// CompareAndSwap -- never takes that mutex, so contention on one fileID
// never blocks callers operating on a different fileID once each fileID's
// state has been created).
//
// Growth characteristic (update, subtask 4.5.3.2): the guards map is now
// bounded, not an ever-growing map keyed by every distinct fileID ever
// guarded -- see acquireGuard/releaseGuard below, mirroring the
// reference-counted eviction policy engine/btree/latch.go's NodeStore.latches
// adopted in subtask 4.5.1.3 (commit 545e827), per .cdr/memory/pending.md's
// "revisit together" note.
//
// The eviction gate here is refs == 0 AND !inProgress.Load() -- simpler than
// NodeStore.latches' refs == 0 AND version == 0 gate, because fileSplitState
// carries no version-like accumulating history for a future reader to lose:
// inProgress is a pure current-state flag, not a monotonically increasing
// counter. There is nothing for a freshly re-created entry to "lose" by
// starting over at the zero value (inProgress == false), UNLESS eviction is
// allowed to happen while a split is still recorded in progress for that
// fileID -- which the gate explicitly forbids. If it were allowed, a second,
// unrelated TryAcquire call for the same fileID could create a fresh
// replacement entry (inProgress defaulting back to false) and immediately
// "win" it, while the original winner still believes it holds exclusive
// rights: a genuine double-acquisition / mutual-exclusion violation, the
// FileGuard analogue of NodeStore.latches' lost-update concern. Requiring
// !inProgress at eviction time closes that: an entry can only ever be
// reclaimed once no goroutine holds an outstanding acquireGuard reference on
// it AND the flag has been explicitly cleared back to false by a Release
// call, i.e. once "no split is in flight and nobody is mid-call" -- exactly
// the set of fileIDs that were merely probed (TryAcquire losers, InProgress
// checks) rather than ones with a currently-active, unreleased split.
type FileGuard struct {
	mu     sync.Mutex
	guards map[uint64]*fileSplitState
}

// NewFileGuard constructs an empty FileGuard with no fileIDs registered
// yet. Per-fileID state is created lazily, on first TryAcquire, Release, or
// InProgress call for that fileID.
func NewFileGuard() *FileGuard {
	return &FileGuard{
		guards: make(map[uint64]*fileSplitState),
	}
}

// getOrCreateLocked returns the fileSplitState for fileID, creating it if
// absent. Callers MUST already hold g.mu. Never touches refs -- mirrors
// engine/btree/latch.go's NodeStore.getOrCreateLocked.
func (g *FileGuard) getOrCreateLocked(fileID uint64) *fileSplitState {
	if g.guards == nil {
		g.guards = make(map[uint64]*fileSplitState)
	}
	s, ok := g.guards[fileID]
	if !ok {
		s = &fileSplitState{}
		g.guards[fileID] = s
	}
	return s
}

// acquireGuard returns the fileSplitState for fileID (creating it if
// necessary) and increments its refs count by one, atomically with the
// lookup/creation under mu. Every call MUST be paired with exactly one later
// call to releaseGuard(fileID, s) once the caller is completely done using
// the returned pointer -- see TryAcquire/Release/InProgress below for the
// exact pairing in each case. Mirrors engine/btree/latch.go's acquireLatch.
func (g *FileGuard) acquireGuard(fileID uint64) *fileSplitState {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.getOrCreateLocked(fileID)
	s.refs++
	return s
}

// releaseGuard balances a prior acquireGuard(fileID) call for the SAME s it
// returned: it decrements s.refs and, only if that drops refs to exactly
// zero AND s.inProgress.Load() == false, removes fileID's entry from the
// registry -- bounding FileGuard.guards' size to (roughly) the number of
// fileIDs currently in flight (an outstanding acquireGuard reference and/or
// an active, unreleased split) rather than every distinct fileID ever passed
// to TryAcquire/Release/InProgress across the FileGuard's entire lifetime.
// See FileGuard's doc comment for why gating on !inProgress (rather than
// evicting unconditionally once refs hits zero) is load-bearing, not an
// optimization. Mirrors engine/btree/latch.go's releaseLatch.
func (g *FileGuard) releaseGuard(fileID uint64, s *fileSplitState) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s.refs--
	if s.refs == 0 && !s.inProgress.Load() {
		// Defense in depth: only delete if the registry still points at this
		// exact object -- always true given the invariant that an entry is
		// never deleted while any acquireGuard reference on it remains
		// outstanding (refs > 0), but costs nothing to check explicitly.
		if cur, ok := g.guards[fileID]; ok && cur == s {
			delete(g.guards, fileID)
		}
	}
}

// TryAcquire attempts to win the right to perform a split for fileID,
// reporting whether this call was the one that won.
//
// This is a genuine compare-and-swap on an atomic.Bool (false -> true),
// not a plain "check the flag, then set it" pair of operations -- the
// latter has a TOCTOU window in which two goroutines can both observe
// "not in progress" before either sets the flag, letting both proceed.
// CompareAndSwap closes that window: of any number of goroutines calling
// TryAcquire for the same fileID concurrently, exactly one observes the
// false-to-true transition and gets true back; every other caller --
// whether it arrived before, during, or after the winner's call --
// observes the flag already set to true and gets false back.
//
// Callers that get false MUST NOT retry-loop waiting for the flag to
// clear, and MUST NOT proceed with the split anyway; the documented
// contract is: winner performs the split, losers back off (e.g. rely on
// the winner's eventual completion, or simply do nothing further for this
// threshold crossing). This subtask provides only the guard primitive;
// 2b.1.3 builds the actual back-off/queueing/status-transition behavior
// for losers on top of it.
//
// The winner is responsible for eventually calling Release(fileID) once
// the split completes (success or failure) so a future threshold crossing
// for the same fileID can win the guard again. TryAcquire itself does not
// know whether the winner ever completed; that lifecycle is out of this
// subtask's scope.
//
// TryAcquire's acquireGuard reference on fileID is deliberately kept open
// (not released) on the winning path -- released only by the winner's later
// Release(fileID) call, which is what pins fileID's registry entry
// (preventing eviction) for the winner's entire "split in progress" window.
// On a losing CAS (someone else already holds the flag), the just-acquired
// reference is released immediately, mirroring engine/btree/latch.go's
// TryLock failure path.
func (g *FileGuard) TryAcquire(fileID uint64) bool {
	s := g.acquireGuard(fileID)
	if s.inProgress.CompareAndSwap(false, true) {
		return true
	}
	g.releaseGuard(fileID, s)
	return false
}

// Release clears fileID's split-in-progress flag, allowing a future
// threshold crossing for that fileID to win TryAcquire again. The winner
// of TryAcquire is expected to call Release exactly once, after the split
// it won has finished (whether it succeeded or failed) -- this subtask
// only provides the guard primitive, not the orchestration that decides
// when a split has "finished"; that is 2b.1.3's job.
//
// Calling Release for a fileID whose flag is not currently set (either
// because it was never acquired, or because it was already released and its
// entry possibly evicted -- see the update note below) is a documented
// no-op: it does not panic or return an error. This is a deliberate design
// choice, not an oversight -- unlike engine/btree/latch.go's nodeLatch.mu (a
// real sync.Mutex, which itself defines double-unlock as caller error),
// FileGuard has no notion of "owner" to attribute a wrongful-release to, so
// there is no well-defined error to raise; treating a redundant Release as a
// no-op keeps the primitive simple and matches its narrow scope.
//
// Update (subtask 4.5.3.2): Release now looks fileID up with a plain,
// non-panicking map read (deliberately NOT engine/btree/latch.go's
// peekLatch idiom, which panics on a missing entry -- that would break the
// no-op-on-miss contract above, which this subtask preserves unchanged). If
// no entry exists, Release simply returns: there is nothing to clear and
// nothing to release. If an entry exists, Release stores false FIRST and
// only THEN calls releaseGuard to decrement/possibly evict.
//
// Correction (fix-cycle 1, issue #40 verification): an earlier version of
// this comment claimed the Store(false)-before-releaseGuard ordering was
// "load-bearing" because reversing it would let a racing TryAcquire win a
// double-acquisition (mirroring engine/btree/latch.go's Unlock, whose
// mu.Unlock()-before-releaseLatch ordering genuinely does prevent a
// double-lock -- see that file's TestNodeLatchUnlockOrderingPreventsDoubleLock).
// That specific claim does NOT hold here, and is not merely unproven -- it is
// structurally unreachable, for a reason that does not apply to btree's case:
// btree's eviction gate is "refs == 0 && version == 0", where version is
// completely independent of the mutex Unlock/Lock actually order around, so
// unlocking the mutex first can genuinely let eviction race ahead of a
// caller's own unlock. FileGuard's eviction gate is "refs == 0 &&
// !inProgress.Load()" -- inProgress is not independent of what Release is
// ordering against, it IS the exact flag Release is clearing. As long as
// inProgress is true, the gate cannot pass, so no fresh replacement entry can
// ever be created for this fileID and no concurrent TryAcquire can ever win
// while the true winner's flag is still set, REGARDLESS of whether
// Store(false) happens before or after releaseGuard. See
// TestFileGuardReleaseOrderingAffectsEvictionProgressNotCorrectness in
// guard_test.go, which deterministically replays the reversed order and
// confirms both halves of this: (1) a concurrent TryAcquire attempted while
// the entry sits in the "refs==0, inProgress still true" state produced by a
// reversed Release still correctly loses (no double-acquisition, ever), and
// (2) what reversing the order actually breaks is eviction PROGRESS, not
// correctness -- releaseGuard's own gate check observes inProgress still true
// at that point and simply declines to evict, leaving the entry parked until
// some later call for the same fileID re-triggers the gate (this is exactly
// what TestFileGuardRegistryBounded caught during verification: registry
// growth, not a mutual-exclusion violation). Storing false first is still the
// preferred ordering -- kept unchanged below -- because it is the only
// ordering under which releaseGuard's own call can ever actually observe
// !inProgress and evict promptly; reversing it doesn't corrupt anything, it
// just defers cleanup to whichever later call happens to touch this fileID
// next.
func (g *FileGuard) Release(fileID uint64) {
	g.mu.Lock()
	s, ok := g.guards[fileID]
	g.mu.Unlock()
	if !ok {
		return
	}
	s.inProgress.Store(false)
	g.releaseGuard(fileID, s)
}

// InProgress reports whether a split is currently recorded as in progress
// for fileID, without attempting to acquire or release the guard. This is
// a read-only observability helper (e.g. for tests and logging) --
// callers deciding whether to attempt a split must use TryAcquire, not
// InProgress-then-TryAcquire, since the latter reintroduces exactly the
// TOCTOU race TryAcquire's atomic CompareAndSwap is designed to avoid.
//
// InProgress brackets its single atomic load in a brief acquireGuard/
// releaseGuard pair purely to participate correctly in the registry's
// refcounted eviction bookkeeping (mirrors engine/btree/latch.go's Version).
// Because inProgress is false for a freshly created entry and this call
// only ever transiently pins it, a probe for a fileID with no split
// currently in progress leaves no permanent trace in the registry -- the
// entry it may have just created is immediately eligible for eviction again
// once this call's own releaseGuard runs (assuming no other concurrent
// caller is also pinning it).
func (g *FileGuard) InProgress(fileID uint64) bool {
	s := g.acquireGuard(fileID)
	v := s.inProgress.Load()
	g.releaseGuard(fileID, s)
	return v
}

// guardRegistrySize returns the current number of entries in
// FileGuard.guards. Test-only observability for this subtask's acceptance
// criterion (the registry must stay bounded rather than growing linearly
// with distinct fileIDs ever guarded) -- see guard_test.go's
// TestFileGuardRegistryBounded. Mirrors engine/btree/latch.go's
// latchRegistrySize.
func (g *FileGuard) guardRegistrySize() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.guards)
}
