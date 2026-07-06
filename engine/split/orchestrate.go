package split

import (
	"errors"
	"fmt"

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
//   - Automatic recovery of a SPLITTING record abandoned by a crashed split
//     holder (FileGuard's in-memory state does not survive a process
//     restart, mirroring catalog.Catalog's own documented "empty index on
//     load" gap) -- BeginSplit refuses to start a second split over an
//     already-SPLITTING record (via its Status precondition check), which
//     prevents a double-split, but does not time out or auto-revert a
//     genuinely stuck record. That recovery story is explicitly deferred.
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
func NewOrchestrator(guard *FileGuard, cat *catalog.Catalog, w *wal.Writer) (*Orchestrator, error) {
	if guard == nil {
		return nil, fmt.Errorf("split: NewOrchestrator: guard must not be nil")
	}
	if cat == nil {
		return nil, fmt.Errorf("split: NewOrchestrator: cat must not be nil")
	}
	if w == nil {
		return nil, fmt.Errorf("split: NewOrchestrator: w must not be nil")
	}
	return &Orchestrator{guard: guard, cat: cat, w: w}, nil
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
func (o *Orchestrator) BeginSplit(fileID uint64) (catalog.CatalogRecord, error) {
	if !o.guard.TryAcquire(fileID) {
		return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d", ErrAlreadySplitting, fileID)
	}

	updated, err := o.transitionStatus(fileID, catalog.StatusActive, catalog.StatusSplitting)
	if err != nil {
		o.guard.Release(fileID)
		if errors.Is(err, errStatusMismatch) {
			return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d", ErrAlreadySplitting, fileID)
		}
		return catalog.CatalogRecord{}, err
	}
	return updated, nil
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
func (o *Orchestrator) EndSplit(fileID uint64, outcome catalog.RecordStatus) (catalog.CatalogRecord, error) {
	if outcome != catalog.StatusActive && outcome != catalog.StatusSplit {
		return catalog.CatalogRecord{}, fmt.Errorf("%w: got %v", ErrUnexpectedStatus, outcome)
	}
	defer o.guard.Release(fileID)

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
