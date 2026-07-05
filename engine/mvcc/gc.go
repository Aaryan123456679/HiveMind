package mvcc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
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

// listVersionFiles returns every version number currently present on disk for fileID
// (skipping any in-progress "*.md.tmp" siblings from writeVersionFile's
// create-temp-then-rename sequence), in no particular order. Mirrors the same
// filename-parsing pattern write.go's scanLatestVersion and write_test.go's
// countVersionFiles already use.
func (vw *VersionWriter) listVersionFiles(fileID uint64) ([]uint64, error) {
	prefix := fmt.Sprintf("%d.v", fileID)

	entries, err := os.ReadDir(vw.dir)
	if err != nil {
		return nil, fmt.Errorf("mvcc: list version files: reading content dir %s: %w", vw.dir, err)
	}

	var versions []uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, versionFileSuffix) {
			continue
		}
		middle := strings.TrimSuffix(strings.TrimPrefix(name, prefix), versionFileSuffix)
		n, err := strconv.ParseUint(middle, 10, 64)
		if err != nil {
			// Not a clean "<fileID>.v<N>.md" name (e.g. a stray leftover *.md.tmp
			// whose suffix happens to still end in ".md" mid-random-suffix); skip
			// rather than fail the whole listing.
			continue
		}
		versions = append(versions, n)
	}
	return versions, nil
}

// RunCompaction reclaims fileID's superseded, no-longer-referenced version files. It
// enumerates every version file on disk for fileID, determines the current version via
// cat.Get (that file is NEVER a deletion candidate, regardless of its own epoch's
// refcount — this is the acceptance criteria's explicit "current version is never
// reclaimed" requirement), and deletes every OTHER version file whose superseding
// commit's epoch is no longer live-referenced by any snapshot.
//
// Epoch<->version mapping (see write.go's VersionWriter.recordVersionEpoch /
// nextRecordedVersionEpoch for the full design rationale): epoch numbering is global
// (shared store-wide across every fileID), not per-fileID, so "epoch == version
// number" is not a valid identification on its own. Instead, CommitVersion records,
// per fileID, which epoch each of its successfully-committed versions became current
// at; RunCompaction looks up the smallest such recorded version number strictly
// greater than a candidate v to find the epoch v was superseded at.
//
// Reclaim decision: for a non-current version v, superseded at epoch E (i.e. E is the
// epoch recorded against the next successfully-committed version after v), v is safe
// to delete iff either no epoch is currently referenced at all (anyReferenced==false),
// or MinReferencedEpoch() >= E. The latter holds because every live snapshot's
// acquired epoch is always >= the epoch that was current when whatever version IT
// pinned became current (see NewSnapshot's doc comment); if the smallest live-
// referenced epoch is already >= E, every live snapshot necessarily acquired its epoch
// at-or-after v's supersession, meaning every live snapshot was looking at v's
// successor or later — never v itself. If no recorded successor is known at all
// (nextRecordedVersionEpoch's ok==false), RunCompaction skips v conservatively rather
// than guess (this also covers a known limitation: the version<->epoch map is
// in-memory only, so it has no history from before this process's VersionWriter was
// constructed).
//
// Any version file whose number is greater than the current version is also skipped:
// it is either an in-flight commit's version file not yet (or never, if it lost its
// CAS race — see CommitVersion's doc comment on orphaned losing retries) published as
// current, and is not safe to reason about from an epoch standpoint.
//
// Concurrency: RunCompaction takes no fileID-wide lock of its own; it is safe to call
// concurrently with ongoing readers/writers for the same fileID (full concurrent
// stress-testing of this is 2a.2.3's scope, not this subtask's) because:
//   - It only ever deletes a version once no live (or future — new snapshots only ever
//     acquire epochs >= the current one) snapshot could possibly still need it, per
//     the reasoning above.
//   - A concurrent CommitVersion for the same fileID only ever writes new,
//     never-reused version numbers and only ever advances (never rewinds)
//     CurrentVersion, so cat.Get's snapshot of "current version" here is always a
//     value that was, or still is, genuinely current.
//   - os.Remove on a path some other, concurrent RunCompaction call already removed
//     returns a "not exist" error, which is treated as a benign no-op, not a failure —
//     so overlapping compaction passes are idempotent.
func RunCompaction(cat *catalog.Catalog, vw *VersionWriter, em *EpochManager, fileID uint64) ([]uint64, error) {
	rec, err := cat.Get(fileID)
	if err != nil {
		return nil, fmt.Errorf("mvcc: run compaction: reading catalog record for fileID %d: %w", fileID, err)
	}
	currentVersion := rec.CurrentVersion

	versions, err := vw.listVersionFiles(fileID)
	if err != nil {
		return nil, fmt.Errorf("mvcc: run compaction: listing version files for fileID %d: %w", fileID, err)
	}

	minRef, anyReferenced := em.MinReferencedEpoch()

	var deleted []uint64
	for _, v := range versions {
		if v == currentVersion {
			continue
		}
		if v > currentVersion {
			continue
		}

		supersededAtEpoch, ok := vw.nextRecordedVersionEpoch(fileID, v)
		if !ok {
			continue
		}

		if anyReferenced && minRef < supersededAtEpoch {
			// Some live snapshot acquired its epoch strictly before v was
			// superseded, so it could conceivably still need v (or something
			// even older); not yet safe to reclaim.
			continue
		}

		path := vw.VersionPath(fileID, v)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return deleted, fmt.Errorf("mvcc: run compaction: removing version %d for fileID %d: %w", v, fileID, err)
		}
		deleted = append(deleted, v)
	}

	return deleted, nil
}
