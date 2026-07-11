package split

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// ErrAlreadySplitting is returned by BeginSplit when either this FileGuard
// already has a split in progress for fileID (the FileGuard.TryAcquire CAS
// lost), or the catalog record's Status is already something other than
// catalog.StatusActive by the time the (uniquely, per the guard) winning
// caller inspects it -- e.g. a SPLITTING record left behind by a previous
// split holder that crashed before calling EndSplit/AbortSplit (see
// architecture-discovery.md's "Crash/stuck-SPLITTING gap" for why this is a
// deliberate refusal, not an attempt at automatic recovery).
var ErrAlreadySplitting = errors.New("split: file already splitting or not in a splittable state")

// ErrNotSplitting is returned by EndSplit when the catalog record's Status is
// not catalog.StatusSplitting at the moment the exit transition is
// attempted -- e.g. EndSplit called without a preceding successful
// BeginSplit, or called twice for the same split attempt.
var ErrNotSplitting = errors.New("split: file is not currently marked SPLITTING")

// ErrSplitInProgress is returned by AdmitWrite when the catalog record's
// Status is catalog.StatusSplitting. Per this package's documented
// "queueing" contract (see Orchestrator's doc comment), a caller that
// receives this error must back off and retry later -- mirroring
// engine/btree's TryLock-miss/restart-from-root contract and
// FileGuard.TryAcquire's loser contract -- rather than proceeding with its
// write or silently dropping it.
var ErrSplitInProgress = errors.New("split: write refused: file is currently splitting")

// ErrUnexpectedStatus is returned by EndSplit when called with an outcome
// that is not one of catalog.StatusActive (abort) or catalog.StatusSplit
// (success) -- a caller-misuse guard, not a runtime race outcome.
var ErrUnexpectedStatus = errors.New("split: EndSplit: outcome must be StatusActive or StatusSplit")

// Orchestrator composes FileGuard (2b.1.2's per-file CAS guard) with the
// catalog's Status field (catalog.StatusActive/StatusSplitting/StatusSplit/
// StatusRedirect, see catalog/record.go) to implement subtask 2b.1.3: marking
// a file SPLITTING in the catalog once a split begins, gating new writers
// while it is SPLITTING, and cleanly transitioning back out of SPLITTING
// once a split attempt ends (whether it succeeds or is aborted).
//
// Scope (see .cdr/runs/2026-07-07/007-implementation/architecture-discovery.md
// for the full writeup): Orchestrator owns the ENTRY into SPLITTING and the
// generic EXIT out of it, plus the AdmitWrite gate that makes "queued rather
// than applied" concretely true for ordinary writers during the SPLITTING
// window. It deliberately does NOT own:
//   - Allocating redirect targets, writing new content/stub files, B+Tree
//     repointing, or graph edges -- all of that is issue #12's ("Atomic
//     split-transaction execution") job. EndSplit(fileID, catalog.StatusSplit)
//     is the primitive #12's execution logic is expected to call once it has
//     actually finished producing that data; Orchestrator does not populate
//     CatalogRecord.RedirectTargetIDs itself.
//   - Wiring AdmitWrite into engine/catalog/content.go's ContentStore.Append
//     (or any other live write path) -- that call site integration is left
//     for whichever later subtask actually connects engine/split to
//     engine/catalog's write path; issue #10's impacted-modules list for
//     2b.1.3 is engine/split/orchestrate.go (+ its test) only.
//   - Cross-process-restart recovery of a SPLITTING record abandoned by a
//     crashed split holder whose lease this same Orchestrator instance never
//     recorded (FileGuard's in-memory state, and this Orchestrator's own
//     in-memory lease bookkeeping, do not survive a process restart,
//     mirroring catalog.Catalog's own documented "empty index on load" gap).
//     Doing so would need a lease-start timestamp persisted on
//     CatalogRecord itself, which is out of this subtask's file scope
//     (engine/split/orchestrate.go only) and left as future work.
//
// Subtask 4.5.3.3 (issue #40) DOES add same-process lease/heartbeat-based
// recovery: BeginSplit records a lease deadline (o.now() + o.leaseDuration)
// each time it wins the guard and transitions a record to StatusSplitting.
// If a later BeginSplit for the same fileID finds the guard still held past
// that deadline, it treats the holder as abandoned (e.g. a goroutine that
// panicked, or simply forgot, between BeginSplit and EndSplit/AbortSplit),
// force-reverts the record back to StatusActive, releases the stale guard
// hold, and retries as a fresh attempt -- see reclaimIfExpired below. This
// prevents the previously-documented "blocks ErrAlreadySplitting forever"
// failure mode for that case; it deliberately does NOT close the
// cross-process-restart gap above, since without a persisted lease-start
// timestamp there is nothing for a freshly-constructed Orchestrator to
// compare its clock against.
//
// "Readers unaffected via MVCC" (the third acceptance criterion) requires no
// new machinery in this file at all: engine/mvcc's Snapshot/NewSnapshot/Read
// pin a fileID's CurrentVersion and read an immutable version file, never
// touching CatalogRecord.Status. A Status transition performed here is
// therefore structurally orthogonal to any in-flight or newly-taken
// Snapshot, as long as nothing concurrently advances CurrentVersion while
// SPLITTING -- which is exactly what AdmitWrite's refusal prevents ordinary
// writers from doing. See orchestrate_test.go's
// reader_snapshot_unaffected_by_splitting subtest for the concrete proof.
type Orchestrator struct {
	guard *FileGuard
	cat   *catalog.Catalog
	w     *wal.Writer

	// now is this Orchestrator's time source, used only for lease bookkeeping
	// (subtask 4.5.3.3). Defaults to time.Now in NewOrchestrator, matching
	// this repo's established "now func() time.Time, injectable via a
	// functional Option" idiom (see engine/rpc/server.go's and
	// engine/rpc/interceptor.go's own now fields) so tests can advance a
	// fake clock deterministically instead of sleeping real wall-clock time.
	now func() time.Time
	// leaseDuration is how long a BeginSplit-held guard/StatusSplitting pair
	// is allowed to remain outstanding before a later BeginSplit for the
	// same fileID is entitled to treat it as abandoned and reclaim it (see
	// reclaimIfExpired). Defaults to DefaultSplitLeaseDuration.
	leaseDuration time.Duration

	// mu guards leases below. Deliberately a separate mutex from
	// FileGuard.mu (guard's own internal lock): lease bookkeeping is purely
	// this Orchestrator's concern (see doc comment above on scope), and
	// giving it its own lock keeps it independent of FileGuard's per-key
	// registry/eviction locking, exactly as FileGuard itself is documented
	// to be independent of Trigger's statelessness.
	mu sync.Mutex
	// leases maps a fileID currently held by a winning BeginSplit call (i.e.
	// one for which EndSplit/AbortSplit has not yet been called) to the wall
	// time after which that hold is considered abandoned. Entries are
	// created in BeginSplit's success path, and removed either by a clean
	// EndSplit/AbortSplit exit or by a later reclaimIfExpired call -- so, at
	// steady state, this map holds at most one entry per fileID with a
	// genuinely in-flight split, not one entry per fileID ever split
	// (unlike the growth characteristic FileGuard.guards had before subtask
	// 4.5.3.2's eviction fix; no analogous eviction policy is needed here
	// because this map is already self-bounding by construction).
	leases map[uint64]time.Time
}

// DefaultSplitLeaseDuration is the lease timeout NewOrchestrator applies when
// no WithLeaseDuration Option is supplied. Chosen to be comfortably longer
// than any legitimate split-execution window (issue #12's ExecuteSplitAtomic
// commit path) while still being short enough that a genuinely abandoned
// SPLITTING record does not block future BeginSplit calls indefinitely.
// Callers with different requirements (e.g. tests wanting a short lease to
// exercise reclaim deterministically via an injected clock, or production
// callers with slower split executors) should override it with
// WithLeaseDuration rather than relying on this specific value staying
// fixed.
const DefaultSplitLeaseDuration = 30 * time.Second

// Option configures optional NewOrchestrator behavior, following this
// repo's established functional-options idiom (see engine/rpc/interceptor.go's
// Option/WithRecorder).
type Option func(*Orchestrator)

// WithClock overrides the Orchestrator's time source used for lease
// bookkeeping (subtask 4.5.3.3). Intended for tests that need to advance
// past a lease deadline deterministically without a real-time sleep; now
// must not be nil.
func WithClock(now func() time.Time) Option {
	return func(o *Orchestrator) {
		if now != nil {
			o.now = now
		}
	}
}

// WithLeaseDuration overrides DefaultSplitLeaseDuration for this
// Orchestrator's lease-based abandoned-SPLITTING recovery (subtask 4.5.3.3).
// A non-positive d is ignored (leaving the default, or whatever was set by
// an earlier Option, in place) rather than producing a lease that expires
// immediately or in the past.
func WithLeaseDuration(d time.Duration) Option {
	return func(o *Orchestrator) {
		if d > 0 {
			o.leaseDuration = d
		}
	}
}

// NewOrchestrator constructs an Orchestrator over guard (the per-file CAS
// guard a caller has typically already used to win the right to attempt a
// split, e.g. via Trigger.Detect -> FileGuard.TryAcquire -> BeginSplit), cat
// (the catalog whose records' Status this Orchestrator transitions), and w
// (the shared WAL that every Status transition is durably logged to before
// being applied, matching this repo's WAL-before-apply invariant). None of
// guard, cat, or w may be nil; NewOrchestrator returns an error rather than
// panicking on invalid construction, matching this repo's convention (see
// e.g. engine/catalog.OpenContentStore).
//
// opts may include WithClock and/or WithLeaseDuration to customize subtask
// 4.5.3.3's lease-based abandoned-SPLITTING recovery; by default the
// Orchestrator uses time.Now and DefaultSplitLeaseDuration.
func NewOrchestrator(guard *FileGuard, cat *catalog.Catalog, w *wal.Writer, opts ...Option) (*Orchestrator, error) {
	if guard == nil {
		return nil, fmt.Errorf("split: NewOrchestrator: guard must not be nil")
	}
	if cat == nil {
		return nil, fmt.Errorf("split: NewOrchestrator: cat must not be nil")
	}
	if w == nil {
		return nil, fmt.Errorf("split: NewOrchestrator: w must not be nil")
	}
	o := &Orchestrator{
		guard:         guard,
		cat:           cat,
		w:             w,
		now:           time.Now,
		leaseDuration: DefaultSplitLeaseDuration,
		leases:        make(map[uint64]time.Time),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o, nil
}

// BeginSplit attempts to win the right to split fileID and, if it wins,
// durably transitions fileID's catalog record's Status to
// catalog.StatusSplitting before returning.
//
// BeginSplit first calls o.guard.TryAcquire(fileID): only the single caller
// that wins this CAS (see FileGuard's doc comment; 2b.1.2's
// TestSplitInProgressCAS already proves exactly one winner under concurrent
// contention) proceeds to inspect and mutate the catalog record below, which
// closes the TOCTOU window that would otherwise exist between reading
// Status and writing StatusSplitting if multiple callers could reach that
// sequence concurrently for the same fileID.
//
// If the guard is won but the catalog record's current Status is not
// catalog.StatusActive (e.g. a SPLITTING/SPLIT/REDIRECT record from a prior,
// possibly crashed, split attempt), BeginSplit releases the guard and
// returns ErrAlreadySplitting: it never forces a second split to begin over
// a record that is not cleanly Active.
//
// On any failure after winning the guard (record not found, encode error,
// WAL append error, or the Status precondition failing), BeginSplit releases
// the guard before returning, so the guard is never left held for a split
// that never actually began.
//
// On success, BeginSplit returns the updated CatalogRecord (Status now
// StatusSplitting) and leaves the guard held: the caller now "owns" this
// split attempt and is responsible for eventually calling EndSplit or
// AbortSplit, which releases the guard as part of transitioning back out of
// SPLITTING.
//
// Subtask 4.5.3.3: if the guard is already held for fileID (i.e. the
// TryAcquire above loses), BeginSplit does not immediately give up. It first
// checks whether the PREVIOUS winner's lease (recorded the last time
// BeginSplit won for this fileID, see recordLease) has expired per this
// Orchestrator's clock. If so, the previous holder is presumed abandoned
// (crashed, or otherwise never called EndSplit/AbortSplit), and
// reclaimIfExpired force-reverts the record to StatusActive and releases the
// stale guard hold before BeginSplit retries TryAcquire once, as a fresh
// attempt. Only if the guard is still busy (no expired lease was found, or
// the reclaim itself lost a race to some other caller) does BeginSplit
// return ErrAlreadySplitting.
func (o *Orchestrator) BeginSplit(fileID uint64) (catalog.CatalogRecord, error) {
	if o.guard.TryAcquire(fileID) {
		return o.finishBeginSplit(fileID)
	}
	if o.reclaimIfExpired(fileID) && o.guard.TryAcquire(fileID) {
		return o.finishBeginSplit(fileID)
	}
	return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d", ErrAlreadySplitting, fileID)
}

// finishBeginSplit performs the transitionStatus(Active -> Splitting) half of
// BeginSplit, assuming the caller has just won o.guard.TryAcquire(fileID).
// On success it records this attempt's lease deadline (see recordLease); on
// failure it releases the just-won guard, exactly as BeginSplit's inline
// logic did before subtask 4.5.3.3 split it out to be shared between the
// direct-win and reclaim-then-retry paths above.
func (o *Orchestrator) finishBeginSplit(fileID uint64) (catalog.CatalogRecord, error) {
	updated, err := o.transitionStatus(fileID, catalog.StatusActive, catalog.StatusSplitting)
	if err != nil {
		o.guard.Release(fileID)
		if errors.Is(err, errStatusMismatch) {
			return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d", ErrAlreadySplitting, fileID)
		}
		return catalog.CatalogRecord{}, err
	}
	o.recordLease(fileID)
	return updated, nil
}

// recordLease sets fileID's lease deadline to o.now() + o.leaseDuration,
// overwriting any prior entry. Called once, by finishBeginSplit, exactly
// when a BeginSplit attempt actually wins both the guard and the
// Active->Splitting transition.
func (o *Orchestrator) recordLease(fileID uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.leases == nil {
		o.leases = make(map[uint64]time.Time)
	}
	o.leases[fileID] = o.now().Add(o.leaseDuration)
}

// clearLease removes fileID's lease entry, if any. Called by EndSplit
// (covering both the AbortSplit and successful-split outcomes) so that a
// clean exit never leaves a stale lease entry behind for reclaimIfExpired to
// find later; also called by reclaimIfExpired itself once it decides to act
// on an expired entry, so a given lease is never reclaimed twice.
func (o *Orchestrator) clearLease(fileID uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.leases, fileID)
}

// reclaimIfExpired reports whether it force-reclaimed an abandoned SPLITTING
// hold for fileID, implementing subtask 4.5.3.3's lease-timeout recovery.
//
// It looks up fileID's recorded lease deadline; if none exists, or the
// deadline has not yet passed per o.now(), it does nothing and returns
// false (this is the common case: either no split has ever begun for
// fileID, or one is genuinely still in progress within its lease window).
//
// If the deadline has passed, reclaimIfExpired removes the lease entry
// (under o.mu, so two concurrent callers can never both observe and act on
// the same expired entry) and then forces the catalog record from
// StatusSplitting back to StatusActive via the same WAL-before-apply
// transitionStatus primitive BeginSplit/EndSplit use, followed by
// o.guard.Release(fileID) to clear the presumed-abandoned holder's stale
// in-memory guard flag. Both steps are best-effort: if transitionStatus
// fails (e.g. the record was not actually StatusSplitting anymore --
// perhaps a legitimate EndSplit/AbortSplit call raced this reclaim and won,
// or some other caller's reclaim got there first), reclaimIfExpired simply
// returns false without touching the guard, leaving the guard's actual
// current holder (whoever that now is) undisturbed.
//
// This is a deliberate, best-effort trade-off inherent to any lease-based
// recovery mechanism (mirroring e.g. distributed lock leases generally): if
// leaseDuration is set shorter than a legitimate split genuinely takes, a
// live (not actually crashed) holder's hold can be force-reclaimed out from
// under it. DefaultSplitLeaseDuration is chosen generously to make that
// unlikely in practice, and WithLeaseDuration lets callers tune it for their
// own executor's expected latency; this subtask does not attempt to detect
// or prevent that false-positive case itself (doing so would need real
// heartbeat renewal from the split holder, which is a larger change than
// this subtask's scope -- see this file's package doc comment).
func (o *Orchestrator) reclaimIfExpired(fileID uint64) bool {
	o.mu.Lock()
	deadline, ok := o.leases[fileID]
	if !ok || o.now().Before(deadline) {
		o.mu.Unlock()
		return false
	}
	delete(o.leases, fileID)
	o.mu.Unlock()

	if _, err := o.transitionStatus(fileID, catalog.StatusSplitting, catalog.StatusActive); err != nil {
		return false
	}
	o.guard.Release(fileID)
	return true
}

// EndSplit durably transitions fileID's catalog record's Status out of
// catalog.StatusSplitting to outcome, and releases the FileGuard held for
// this fileID (whether the transition itself succeeds or fails), matching
// FileGuard's documented "winner ... calls Release ... once the split
// completes (success or failure)" contract.
//
// outcome must be catalog.StatusActive (the split is being abandoned/
// aborted; see AbortSplit) or catalog.StatusSplit (the split completed
// successfully -- expected to be called by issue #12's execution logic once
// it has durably committed the real split content and redirect data; this
// Orchestrator does not populate CatalogRecord.RedirectTargetIDs itself).
// Any other outcome value is a caller-misuse error (ErrUnexpectedStatus),
// returned WITHOUT releasing the guard or touching the catalog record: it
// does not correspond to a real split attempt ending, so there is nothing
// to unstick.
//
// If the record's current Status is not catalog.StatusSplitting when
// EndSplit is called (e.g. called without a preceding BeginSplit, or called
// twice), EndSplit returns ErrNotSplitting -- but the guard is still
// released in this case (via a deferred release after outcome validation),
// so a caller that raced or mis-tracked state does not leave the guard
// stuck.
//
// EndSplit also always clears fileID's lease entry (subtask 4.5.3.3's
// abandoned-SPLITTING recovery bookkeeping, see recordLease/clearLease),
// again regardless of outcome, so a clean exit never leaves a stale lease
// deadline behind for a later BeginSplit's reclaimIfExpired to trip over.
func (o *Orchestrator) EndSplit(fileID uint64, outcome catalog.RecordStatus) (catalog.CatalogRecord, error) {
	if outcome != catalog.StatusActive && outcome != catalog.StatusSplit {
		return catalog.CatalogRecord{}, fmt.Errorf("%w: got %v", ErrUnexpectedStatus, outcome)
	}
	defer o.guard.Release(fileID)
	defer o.clearLease(fileID)

	updated, err := o.transitionStatus(fileID, catalog.StatusSplitting, outcome)
	if err != nil {
		if errors.Is(err, errStatusMismatch) {
			return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d", ErrNotSplitting, fileID)
		}
		return catalog.CatalogRecord{}, err
	}
	return updated, nil
}

// AbortSplit is a convenience wrapper for EndSplit(fileID, catalog.StatusActive):
// the common "give up, no actual split happened" exit path, e.g. when a
// split attempt fails before issue #12's execution logic ever durably
// commits anything.
func (o *Orchestrator) AbortSplit(fileID uint64) (catalog.CatalogRecord, error) {
	return o.EndSplit(fileID, catalog.StatusActive)
}

// AdmitWrite is the write-admission gate implementing this subtask's "new
// writer requests for the file are queued rather than applied" acceptance
// criterion: it reads fileID's current catalog record and refuses with
// ErrSplitInProgress if Status is catalog.StatusSplitting, so a writer path
// that calls AdmitWrite before performing its actual write never silently
// applies a mutation while a split is in flight.
//
// Per this package's documented "queueing" contract (see Orchestrator's doc
// comment on why this codebase's established idiom -- engine/btree's
// TryLock-miss/restart-from-root pattern, and FileGuard.TryAcquire's own
// loser contract -- is a caller-retries sentinel error rather than a new
// blocking channel/condvar primitive), a caller that receives
// ErrSplitInProgress is expected to back off and retry later, not proceed
// with its write or silently drop it.
//
// AdmitWrite is a point-in-time check, not a CAS: it does not itself make
// "check status, then write" atomic end-to-end against a concurrent
// BeginSplit racing in between. That stronger atomicity is unnecessary for
// this subtask's scope (an entry gate a writer consults before its own
// write, not the write pipeline itself) and is superseded once issue #12's
// single atomic WAL-covered commit lands (which is what actually releases
// queued writers on commit, per issue #10's acceptance criteria for this
// subtask and issue #12's for that one).
//
// Any CatalogRecord.Status other than catalog.StatusSplitting -- including
// catalog.StatusSplit and catalog.StatusRedirect -- is NOT refused here:
// writer semantics for an already-SPLIT/REDIRECT file (e.g. redirecting the
// write to a new target fileID) belong to issue #12's scope, not this
// subtask's. AdmitWrite's contract is narrowly "refuse exactly while
// SPLITTING", matching this subtask's acceptance criterion precisely.
func (o *Orchestrator) AdmitWrite(fileID uint64) (catalog.CatalogRecord, error) {
	rec, err := o.cat.Get(fileID)
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: admit write: fileID %d: %w", fileID, err)
	}
	if rec.Status == catalog.StatusSplitting {
		return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d", ErrSplitInProgress, fileID)
	}
	return rec, nil
}

// errStatusMismatch is transitionStatus's internal sentinel for "the record's
// current Status did not equal the required precondition" -- translated by
// BeginSplit/EndSplit into their own public sentinel errors (ErrAlreadySplitting
// / ErrNotSplitting respectively), so this package's exported error surface
// stays specific to each call site rather than exposing one generic error for
// two different-meaning refusals.
var errStatusMismatch = errors.New("split: catalog record status precondition not met")

// transitionStatus durably transitions fileID's catalog record's Status from
// requiredCurrent to newStatus, refusing with errStatusMismatch (never
// mutating anything) if the record's actual current Status is not
// requiredCurrent when read. The transition itself follows this repo's
// established WAL-before-apply idiom (see engine/catalog/content.go's
// ContentStore.Append/createWithHook): the updated record is durably logged
// to the WAL via wal.NewCatalogPutRecord + wal.AppendAndApply, and only once
// that succeeds is it applied via cat.Put, so the Status transition itself
// is crash-durable and WAL-replay-safe on the same terms as every other
// catalog mutation in this codebase.
//
// This is a read-then-conditional-write sequence, not a dedicated CAS method
// on Catalog (unlike Catalog.CompareAndSwapCurrentVersion): it is safe here
// because BeginSplit/EndSplit's callers are always externally serialized per
// fileID by FileGuard (TryAcquire for BeginSplit's entry, and EndSplit is
// only ever meaningfully called by whichever single caller currently holds
// the guard for fileID), so no other Orchestrator call for the SAME fileID
// can interleave between this function's cat.Get and cat.Put. A concurrent
// mutation of the SAME fileID's record via some OTHER path entirely (e.g. a
// direct cat.Put bypassing this package) is not guarded against here, exactly
// as Catalog's own doc comments already note is the case for any of its
// direct callers that don't use its CAS primitives.
func (o *Orchestrator) transitionStatus(fileID uint64, requiredCurrent, newStatus catalog.RecordStatus) (catalog.CatalogRecord, error) {
	rec, err := o.cat.Get(fileID)
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: transition status: reading fileID %d: %w", fileID, err)
	}
	if rec.Status != requiredCurrent {
		return catalog.CatalogRecord{}, errStatusMismatch
	}

	updated := rec
	updated.Status = newStatus

	encoded, err := updated.Encode()
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: transition status: encoding fileID %d: %w", fileID, err)
	}

	walRec := wal.NewCatalogPutRecord(fileID, encoded)
	if _, err := wal.AppendAndApply(o.w, walRec, func() error {
		if err := o.cat.Put(updated); err != nil {
			return fmt.Errorf("committing catalog record fileID %d: %w", fileID, err)
		}
		return nil
	}); err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: transition status: %w", err)
	}

	return updated, nil
}
