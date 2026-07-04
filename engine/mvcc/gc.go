package mvcc

import (
	"fmt"
	"sync"
)

// EpochManager tracks a single, store-wide (not per-fileID) monotonically increasing
// epoch counter, plus a reference count per epoch, implementing the "reference-counted
// snapshot epochs" visibility scheme described in docs/LLD/mvcc.md's "Garbage
// collection" section: "each snapshot increments an epoch's refcount on start and
// decrements on completion; a version is eligible for GC once its epoch's refcount
// reaches zero and it is not the current version."
//
// Epoch numbering: epoch 0 is reserved as an unambiguous "never acquired" zero value;
// the counter starts at 1, so AcquireCurrentEpoch always returns a valid (>=1) epoch
// even before any AdvanceEpoch call. Numerically, epoch N means "the state of the
// world after the Nth call to AdvanceEpoch" — a coarse, store-wide visibility boundary,
// not scoped to any single fileID (see architecture-discovery.md / plan.md for this
// subtask's full reasoning on why a global counter, rather than a per-fileID one, is
// sufficient for identifying versions no longer referenced by ANY live snapshot).
//
// Design note (2a.2.1): EpochManager is a standalone, additive primitive. It is
// deliberately NOT wired into Snapshot (read.go) or VersionWriter.CommitVersion
// (write.go) yet — that end-to-end integration (CommitVersion calling AdvanceEpoch,
// NewSnapshot calling AcquireCurrentEpoch, a future Snapshot.Close calling Release) is
// left to the compactor subtasks (2a.2.2/2a.2.3) that actually consume
// MinReferencedEpoch. This keeps 2a.1.*'s existing tests (which never call any kind of
// Close()) untouched and unaffected by this subtask.
//
// EpochManager is safe for concurrent use by multiple goroutines. A single mutex (not
// per-epoch locking or a sync.Map) guards both current and refcounts together,
// because AcquireCurrentEpoch must read current and bump refcounts[current] as one
// atomic unit — unlike VersionWriter's per-fileID sharding, epoch bookkeeping is
// inherently one linearizable piece of shared state, not many independent ones.
type EpochManager struct {
	mu        sync.Mutex
	current   uint64
	refcounts map[uint64]int64
}

// NewEpochManager returns an EpochManager with its epoch counter initialized to 1 (see
// EpochManager's doc comment for why 0 is reserved as a sentinel) and no live
// references.
func NewEpochManager() *EpochManager {
	return &EpochManager{
		current:   1,
		refcounts: make(map[uint64]int64),
	}
}

// CurrentEpoch returns the epoch number that AcquireCurrentEpoch would hand out right
// now, without acquiring it.
func (em *EpochManager) CurrentEpoch() uint64 {
	em.mu.Lock()
	defer em.mu.Unlock()
	return em.current
}

// AdvanceEpoch increments the global epoch counter by one and returns the new current
// epoch. Intended (per plan.md) to be called once per successful CommitVersion CAS by a
// later subtask, so epoch boundaries line up with "some fileID's CurrentVersion pointer
// advanced," the visibility boundary MinReferencedEpoch-based GC decisions are checked
// against.
func (em *EpochManager) AdvanceEpoch() uint64 {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.current++
	return em.current
}

// AcquireCurrentEpoch increments the current epoch's refcount by one — the "increment
// on start" half of this subtask's acceptance criteria — and returns which epoch number
// was acquired, so the caller (e.g. a future Snapshot) can later Release exactly that
// epoch regardless of how far the global counter advances afterward.
func (em *EpochManager) AcquireCurrentEpoch() uint64 {
	em.mu.Lock()
	defer em.mu.Unlock()
	epoch := em.current
	em.refcounts[epoch]++
	return epoch
}

// Release decrements epoch's refcount by one — the "decrement on completion" half of
// this subtask's acceptance criteria. Once an epoch's refcount reaches zero, its entry
// is removed from the internal map entirely, so RefCount uniformly reports 0 for both
// "never acquired" and "acquired then fully released" epochs, and MinReferencedEpoch
// never has to skip over stale zeroed entries.
//
// Release returns a non-nil error, rather than panicking, if epoch's refcount is
// already zero (a double-release / over-release): this is a library-level API that
// ordinary caller bugs (e.g. a duplicate Close() call) could trigger, and an error is
// both more idiomatic Go and easier to assert on in tests than crashing the whole
// process. Refcounts are hard-guaranteed to never go negative: the count is verified
// to be > 0 before it is ever decremented.
func (em *EpochManager) Release(epoch uint64) error {
	em.mu.Lock()
	defer em.mu.Unlock()

	count, ok := em.refcounts[epoch]
	if !ok || count <= 0 {
		return fmt.Errorf("mvcc: epoch manager: release epoch %d: refcount is already zero (double-release?)", epoch)
	}

	count--
	if count == 0 {
		delete(em.refcounts, epoch)
	} else {
		em.refcounts[epoch] = count
	}
	return nil
}

// RefCount returns epoch's current reference count: 0 for an epoch that was never
// acquired, or that was acquired and has since been fully released.
func (em *EpochManager) RefCount(epoch uint64) int64 {
	em.mu.Lock()
	defer em.mu.Unlock()
	return em.refcounts[epoch]
}

// MinReferencedEpoch returns the smallest epoch number with a nonzero refcount, and
// ok=true, or ok=false if no epoch currently has any live references at all. Future GC
// subtasks (2a.2.2/2a.2.3) use this to decide which superseded versions are safe to
// reclaim: any version superseded strictly before this epoch cannot be visible to any
// live snapshot, since every live snapshot acquired an epoch at or after it.
func (em *EpochManager) MinReferencedEpoch() (uint64, bool) {
	em.mu.Lock()
	defer em.mu.Unlock()

	var (
		min   uint64
		found bool
	)
	for epoch, count := range em.refcounts {
		if count <= 0 {
			continue
		}
		if !found || epoch < min {
			min = epoch
			found = true
		}
	}
	return min, found
}
