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
type fileSplitState struct {
	inProgress atomic.Bool
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
// Growth characteristic: like NodeStore.latches, the guards map here is
// never evicted -- entries accumulate for every distinct fileID ever
// guarded, for the lifetime of the FileGuard. This is a deliberate,
// deferred limitation matching that existing precedent (see
// .cdr/memory/pending.md); this subtask does not add eviction/bounding, and
// should not be over-engineered to do so.
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

// stateFor returns the fileSplitState associated with fileID, lazily
// creating it on first access. Safe for concurrent use; the same
// *fileSplitState is always returned for a given fileID on a given
// FileGuard (mirrors NodeStore.latchFor's idiom in engine/btree/latch.go).
func (g *FileGuard) stateFor(fileID uint64) *fileSplitState {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.guards[fileID]
	if !ok {
		s = &fileSplitState{}
		g.guards[fileID] = s
	}
	return s
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
func (g *FileGuard) TryAcquire(fileID uint64) bool {
	return g.stateFor(fileID).inProgress.CompareAndSwap(false, true)
}

// Release clears fileID's split-in-progress flag, allowing a future
// threshold crossing for that fileID to win TryAcquire again. The winner
// of TryAcquire is expected to call Release exactly once, after the split
// it won has finished (whether it succeeded or failed) -- this subtask
// only provides the guard primitive, not the orchestration that decides
// when a split has "finished"; that is 2b.1.3's job.
//
// Calling Release for a fileID whose flag is not currently set (either
// because it was never acquired, or because it was already released) is a
// documented no-op: it unconditionally stores false and does not panic or
// return an error. This is a deliberate design choice, not an oversight --
// unlike engine/btree/latch.go's nodeLatch.mu (a real sync.Mutex, which
// itself defines double-unlock as caller error), FileGuard has no notion
// of "owner" to attribute a wrongful-release to, so there is no
// well-defined error to raise; treating a redundant Release as a no-op
// keeps the primitive simple and matches its narrow scope.
func (g *FileGuard) Release(fileID uint64) {
	g.stateFor(fileID).inProgress.Store(false)
}

// InProgress reports whether a split is currently recorded as in progress
// for fileID, without attempting to acquire or release the guard. This is
// a read-only observability helper (e.g. for tests and logging) --
// callers deciding whether to attempt a split must use TryAcquire, not
// InProgress-then-TryAcquire, since the latter reintroduces exactly the
// TOCTOU race TryAcquire's atomic CompareAndSwap is designed to avoid.
func (g *FileGuard) InProgress(fileID uint64) bool {
	return g.stateFor(fileID).inProgress.Load()
}
