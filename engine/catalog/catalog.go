package catalog

import (
	"errors"
	"fmt"
	"sync"
)

// numStripes is the number of striped mutexes used to guard concurrent access to
// individual CatalogRecords, per docs/LLD/catalog.md: "Striped mutexes (~256 stripes,
// hashed by fileID) instead of one global lock, so unrelated files never contend on
// the same lock."
const numStripes = 256

// numPageStripes is the number of striped mutexes guarding the read-modify-write
// page sequence (ReadPage -> mutate in-memory Page -> WritePage) against concurrent
// callers that happen to target the SAME physical pageID. This is necessary and
// orthogonal to numStripes/stripeFor above: many distinct fileIDs' records can be
// packed onto the same physical page (see page.go's slotted layout), so two
// operations on DIFFERENT fileIDs' stripes can still race on the same underlying
// page (e.g. one fileID's tombstone racing another fileID's insert into the same
// shared active page) unless that specific pageID's read-modify-write sequence is
// serialized. FileManager's own internal lock (file.go's mu) does not and should not
// cover this: it only protects FileManager's own bookkeeping (highestAllocated,
// bitmap), not the caller-orchestrated, multi-call page mutation sequences that live
// here in Catalog. Keying by pageID (rather than reusing the fileID stripes) keeps
// this narrowly scoped to pages that are ACTUALLY shared, so operations on different
// fileIDs whose records live on different pages still proceed without blocking each
// other.
const numPageStripes = 256

// ErrNotFound is the sentinel error returned by Get and Delete when the requested
// fileID has no corresponding CatalogRecord. Callers should use errors.Is(err,
// ErrNotFound) rather than string-matching; both Get and Delete wrap it with the
// specific fileID for diagnostic context via fmt.Errorf's %w.
var ErrNotFound = errors.New("catalog: fileID not found")

// location records where a CatalogRecord's encoded bytes physically live: which page,
// and which slot within that page (see page.go for the slotted-page layout).
type location struct {
	pageID uint64
	slotID int
}

// Catalog is the striped-mutex CRUD API over CatalogRecords, built on top of the
// already-verified Page (1.1.2), FileManager (1.1.3), and IDAllocator (1.1.4)
// primitives. See docs/LLD/catalog.md for the on-disk design this wires together.
//
// This subtask (1.1.5) is purely a CRUD wiring layer: Put/Get/Delete operate on a
// single current record per fileID with no history/versioning of that fileID's past
// records (MVCC versioning is a separate later subtask, see engine/mvcc/), no split
// logic (engine/split/), and no WAL logging (engine/wal/) — none of those are
// implemented here.
//
// Locking model (three logically distinct locks, each protecting a different
// resource — documented explicitly because it is easy to conflate "striped locks"
// with "the index lock"):
//
//  1. stripes [numStripes]sync.Mutex, keyed by stripeFor(fileID) (fileID % numStripes).
//     This is the "striped-mutex catalog CRUD" lock from docs/LLD/catalog.md: it
//     protects the per-fileID read-modify-write critical section (e.g. two concurrent
//     Puts to the same fileID, or a Put racing a Delete on the same fileID) against
//     torn reads and lost updates. Operations on fileIDs that hash to DIFFERENT
//     stripes never contend on this lock.
//
//  2. indexMu sync.RWMutex, guarding the index map itself (fileID -> location). This
//     is a completely different concern from (1): a Go map is never safe for
//     concurrent access regardless of how many per-fileID stripes exist above it, so
//     the lookup structure needs its own (brief, map-operation-only) protection. This
//     is NOT one of the 256 stripes and must not be conflated with them — holding it
//     is only ever brief (a map read or map write), never for the duration of a
//     page I/O.
//
//  3. FileManager's own internal lock (fm.go's unexported mu field), which Catalog
//     does NOT need to take itself and has no access to. FileManager (1.1.3) now
//     synchronizes its own genuinely shared, file-wide bookkeeping state
//     (highestAllocated, the free-list bitmap) internally, guarding only the brief
//     bitmap-check/bitmap-mutation critical sections inside AllocatePage, FreePage,
//     and the highestAllocated read inside validDataPageID — never the actual
//     pread/pwrite/fsync page I/O, which is safe to run concurrently across distinct
//     pages/fileIDs without any coordination (different pages occupy non-overlapping
//     file regions). This means Catalog.readSlot/tombstone/insert/tryInsertInto call
//     FileManager methods (ReadPage/WritePage/AllocatePage/FreePage) directly, with no
//     additional locking of their own around them: an earlier version of this file had
//     a caller-side fmMu sync.Mutex wrapping every FileManager call, which incorrectly
//     serialized ALL operations (including the expensive synchronous fsync in
//     WritePage) across every fileID regardless of which page or stripe they touched —
//     directly contradicting docs/LLD/catalog.md's "unrelated files never contend on
//     the same lock" design goal. That caller-side lock has been removed; the narrow
//     fix now lives inside FileManager itself (see file.go's FileManager doc comment
//     and the mu field), where it belongs, since the state being protected is
//     FileManager's own, not Catalog's.
//
// Known gap (intentionally out of scope for 1.1.5, not a regression introduced here):
// NewCatalog does not scan .meta/catalog.dat on load to rebuild the in-memory index
// from whatever records already exist on disk; FileManager (1.1.3) exposes no page-
// enumeration API (only AllocatePage/FreePage/ReadPage/WritePage), and adding one is
// out of this subtask's impacted-modules scope (engine/catalog/catalog.go,
// engine/catalog/catalog_test.go only). A fresh Catalog therefore starts with an
// empty in-memory index; only records Put during the current process's Catalog
// lifetime are reachable via Get/Delete. The underlying page bytes ARE durably
// persisted (WritePage's WriteAt+Sync), so no data is lost on disk — only the index
// needed to find it again without a full scan is process-lifetime-scoped for now.
// Rebuilding the index durably across restarts is deferred to whichever later subtask
// introduces either a FileManager page-enumeration API or a persisted directory/index
// page in catalog.dat itself (plausibly alongside wal/'s recovery story). This
// mirrors, and is adjacent to, the cross-check gap flagged by 1.1.4's verification
// (IDAllocator's sidecar high-water-mark has no cross-check against catalog.dat's
// actual max FileID if the sidecar goes missing): Catalog is now the component that
// actually writes FileID values into pages, so it is a natural place a future subtask
// could add such a cross-check (e.g. reconciling IDAllocator's high-water-mark against
// the max FileID found during an index rebuild scan). Fixing that cross-check is
// explicitly NOT done in this subtask — noted here only as a cross-reference.
type Catalog struct {
	fm *FileManager

	stripes [numStripes]sync.Mutex

	// pageStripes guards the read-modify-write page sequence (ReadPage -> mutate ->
	// WritePage) against concurrent callers targeting the same physical pageID, keyed
	// by pageStripeFor(pageID). See numPageStripes' doc comment above for why this is
	// necessary and distinct from both stripes (keyed by fileID) and FileManager's
	// own internal lock (keyed by nothing — it only guards FileManager's own
	// bookkeeping, not page contents).
	pageStripes [numPageStripes]sync.Mutex

	indexMu sync.RWMutex
	index   map[uint64]location

	// activeMu guards activePageID: the single shared "current page being appended
	// into for new inserts" cursor. A single shared active page (rather than one per
	// stripe) is a deliberate simplicity choice for this subtask; per-stripe active
	// pages would reduce contention further but are an optimization left for later,
	// once real throughput needs justify the added bookkeeping complexity.
	activeMu     sync.Mutex
	activePageID uint64 // 0 means "no active page allocated yet this process"
}

// NewCatalog wraps fm (an already-open FileManager, see file.go) in a Catalog CRUD
// layer. See the Catalog doc comment above for the "empty index on load" limitation.
func NewCatalog(fm *FileManager) *Catalog {
	return &Catalog{
		fm:    fm,
		index: make(map[uint64]location),
	}
}

// stripeFor returns the record-stripe index for fileID. Plain modulo (rather than a
// proper hash of fileID) is a deliberate, documented simplicity choice: fileIDs are
// allocated by IDAllocator.Next() (idalloc.go) as a monotonically increasing counter,
// so they are already well-distributed across stripes by allocation order in the
// common case. A hash-based stripe selection (e.g. fnv/xxhash of fileID) would be
// more robust against adversarial or pathological ID patterns, should that property
// ever matter; it is not required for this subtask's acceptance criteria.
func stripeFor(fileID uint64) uint64 {
	return fileID % numStripes
}

// pageStripeFor returns the page-stripe index for pageID (see numPageStripes' doc
// comment for what this protects and why it's keyed separately from stripeFor).
func pageStripeFor(pageID uint64) uint64 {
	return pageID % numPageStripes
}

// Put inserts or overwrites the CatalogRecord for rec.FileID. If a record already
// exists for this fileID, Put always deletes the old slot and inserts a fresh one
// (delete-then-reinsert) rather than attempting an in-place update — a deliberate
// simplicity-over-efficiency tradeoff. CatalogRecord.Encode always produces a fixed
// RecordEncodedSize-byte buffer, so in-place update-if-it-fits is a plausible future
// optimization (mirroring the deferred-compaction language already used in page.go's
// doc comments), but is not implemented here: Page.InsertSlot's tombstone-reuse path
// already reclaims a same-page deleted slot's space for a subsequent insert, so the
// simple approach does not leak space, it just doesn't guarantee reusing the exact
// same physical slot. Put overwrites the current record for a fileID, full stop — no
// history/versioning of a fileID's past records is kept by this subtask.
func (c *Catalog) Put(rec CatalogRecord) error {
	if rec.FileID == InvalidFileID {
		return fmt.Errorf("catalog: put: invalid fileID %d", rec.FileID)
	}

	data, err := rec.Encode()
	if err != nil {
		return fmt.Errorf("catalog: put: encoding fileID %d: %w", rec.FileID, err)
	}

	stripe := stripeFor(rec.FileID)
	c.stripes[stripe].Lock()
	defer c.stripes[stripe].Unlock()

	c.indexMu.RLock()
	oldLoc, hadOld := c.index[rec.FileID]
	c.indexMu.RUnlock()

	if hadOld {
		if err := c.tombstone(oldLoc); err != nil {
			return fmt.Errorf("catalog: put: removing old slot for fileID %d: %w", rec.FileID, err)
		}
	}

	newLoc, err := c.insert(data)
	if err != nil {
		return fmt.Errorf("catalog: put: inserting fileID %d: %w", rec.FileID, err)
	}

	c.indexMu.Lock()
	c.index[rec.FileID] = newLoc
	c.indexMu.Unlock()

	return nil
}

// Get returns the current CatalogRecord for fileID, or a wrapped ErrNotFound if no
// record exists for it. The record's stripe lock is briefly held (in addition to the
// index lock) so that Get is linearizable with a concurrent Put's delete-then-
// reinsert sequence for the SAME fileID: without it, a Get could otherwise observe the
// index mid-update (after the old slot's tombstone but before the new slot's insert
// completes) and either find nothing when a record does logically exist, or read a
// stale/half-updated location.
func (c *Catalog) Get(fileID uint64) (CatalogRecord, error) {
	if fileID == InvalidFileID {
		return CatalogRecord{}, fmt.Errorf("catalog: get: invalid fileID %d", fileID)
	}

	stripe := stripeFor(fileID)
	c.stripes[stripe].Lock()
	defer c.stripes[stripe].Unlock()

	c.indexMu.RLock()
	loc, ok := c.index[fileID]
	c.indexMu.RUnlock()
	if !ok {
		return CatalogRecord{}, fmt.Errorf("catalog: get: %w: fileID %d", ErrNotFound, fileID)
	}

	data, err := c.readSlot(loc)
	if err != nil {
		return CatalogRecord{}, fmt.Errorf("catalog: get: reading fileID %d: %w", fileID, err)
	}

	rec, err := Decode(data)
	if err != nil {
		return CatalogRecord{}, fmt.Errorf("catalog: get: decoding fileID %d: %w", fileID, err)
	}
	return rec, nil
}

// Delete removes the CatalogRecord for fileID, or returns a wrapped ErrNotFound if no
// record exists for it (never panics, never silently no-ops on a nonexistent fileID).
func (c *Catalog) Delete(fileID uint64) error {
	if fileID == InvalidFileID {
		return fmt.Errorf("catalog: delete: invalid fileID %d", fileID)
	}

	stripe := stripeFor(fileID)
	c.stripes[stripe].Lock()
	defer c.stripes[stripe].Unlock()

	c.indexMu.Lock()
	loc, ok := c.index[fileID]
	if ok {
		delete(c.index, fileID)
	}
	c.indexMu.Unlock()

	if !ok {
		return fmt.Errorf("catalog: delete: %w: fileID %d", ErrNotFound, fileID)
	}

	if err := c.tombstone(loc); err != nil {
		return fmt.Errorf("catalog: delete: fileID %d: %w", fileID, err)
	}
	return nil
}

// CompareAndSwapCurrentVersion atomically advances the CatalogRecord for fileID's
// CurrentVersion field from expected to newVersion, but ONLY if the record's
// CurrentVersion currently equals expected. This is the CAS hook point
// docs/LLD/mvcc.md's "Write path" describes ("An atomic CAS swaps 'current version'
// pointer in catalog record fileID once new version durably written"): callers
// (engine/mvcc's VersionWriter.CommitVersion) durably write a new version file FIRST,
// then call this to publish it as the current version.
//
// The read-check-write sequence is held atomic by reusing fileID's existing stripe
// lock (stripes[stripeFor(fileID)]) — the SAME lock Get and Put already serialize on
// for this fileID — rather than adding a new lock, so a CompareAndSwapCurrentVersion
// racing a concurrent Put or another CompareAndSwapCurrentVersion on the SAME fileID
// can never interleave: Catalog does not need to expose a way to hold Get's lock
// across a caller-side conditional Put, because that whole sequence lives here,
// inside a single stripe-lock critical section.
//
// Returns:
//   - (true, updatedRecord, nil) if the swap succeeded: CurrentVersion is now
//     newVersion (updatedRecord reflects this).
//   - (false, currentRecord, nil) if the swap was refused because CurrentVersion did
//     NOT equal expected (some other write already advanced it first); currentRecord
//     is the actual current record as of this call, so the caller can inspect the
//     winner's state (e.g. its CurrentVersion) to decide how to retry.
//   - (false, CatalogRecord{}, err) if fileID has no record, or reading/encoding/
//     writing fails.
func (c *Catalog) CompareAndSwapCurrentVersion(fileID, expected, newVersion uint64) (bool, CatalogRecord, error) {
	if fileID == InvalidFileID {
		return false, CatalogRecord{}, fmt.Errorf("catalog: cas current version: invalid fileID %d", fileID)
	}

	stripe := stripeFor(fileID)
	c.stripes[stripe].Lock()
	defer c.stripes[stripe].Unlock()

	c.indexMu.RLock()
	loc, ok := c.index[fileID]
	c.indexMu.RUnlock()
	if !ok {
		return false, CatalogRecord{}, fmt.Errorf("catalog: cas current version: %w: fileID %d", ErrNotFound, fileID)
	}

	data, err := c.readSlot(loc)
	if err != nil {
		return false, CatalogRecord{}, fmt.Errorf("catalog: cas current version: reading fileID %d: %w", fileID, err)
	}
	rec, err := Decode(data)
	if err != nil {
		return false, CatalogRecord{}, fmt.Errorf("catalog: cas current version: decoding fileID %d: %w", fileID, err)
	}

	if rec.CurrentVersion != expected {
		// Lost the race: someone else's write already advanced CurrentVersion past
		// what this caller started from. Return the actual current record unchanged
		// so the caller can retry against the winner's state instead of corrupting
		// or silently overwriting it.
		return false, rec, nil
	}

	rec.CurrentVersion = newVersion
	newData, err := rec.Encode()
	if err != nil {
		return false, CatalogRecord{}, fmt.Errorf("catalog: cas current version: encoding fileID %d: %w", fileID, err)
	}

	if err := c.tombstone(loc); err != nil {
		return false, CatalogRecord{}, fmt.Errorf("catalog: cas current version: removing old slot for fileID %d: %w", fileID, err)
	}
	newLoc, err := c.insert(newData)
	if err != nil {
		return false, CatalogRecord{}, fmt.Errorf("catalog: cas current version: inserting fileID %d: %w", fileID, err)
	}

	c.indexMu.Lock()
	c.index[fileID] = newLoc
	c.indexMu.Unlock()

	return true, rec, nil
}

// readSlot reads and returns the raw encoded bytes stored at loc. No additional
// locking around the FileManager call is needed here: FileManager synchronizes its
// own internal shared state itself (see file.go), and distinct pages'/fileIDs' I/O
// proceeds concurrently without contention.
func (c *Catalog) readSlot(loc location) ([]byte, error) {
	pageStripe := pageStripeFor(loc.pageID)
	c.pageStripes[pageStripe].Lock()
	defer c.pageStripes[pageStripe].Unlock()

	page, err := c.fm.ReadPage(loc.pageID)
	if err != nil {
		return nil, err
	}
	return page.ReadSlot(loc.slotID)
}

// tombstone deletes the slot at loc and durably writes the page back. The
// pageStripes lock (keyed by loc.pageID) is required here, in addition to
// FileManager needing no locking of its own (see readSlot's comment above):
// tombstone's ReadPage -> mutate -> WritePage sequence must be atomic with respect
// to any other operation (insert, another tombstone) touching the SAME physical
// page, since distinct fileIDs' records commonly share a page.
func (c *Catalog) tombstone(loc location) error {
	pageStripe := pageStripeFor(loc.pageID)
	c.pageStripes[pageStripe].Lock()
	defer c.pageStripes[pageStripe].Unlock()

	page, err := c.fm.ReadPage(loc.pageID)
	if err != nil {
		return err
	}
	if err := page.DeleteSlot(loc.slotID); err != nil {
		return err
	}
	return c.fm.WritePage(loc.pageID, page)
}

// insert appends data as a new slot, reusing the shared active page if it has room,
// or allocating a fresh page via FileManager.AllocatePage otherwise. It returns the
// location the data was written to, after durably writing the page (WritePage's
// WriteAt+Sync) so the insert is durable before insert returns. activeMu serializes
// concurrent inserts' view of "which page is currently active"; FileManager calls
// need no additional locking from Catalog (see readSlot's comment above).
func (c *Catalog) insert(data []byte) (location, error) {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()

	if c.activePageID != 0 {
		if loc, ok, err := c.tryInsertInto(c.activePageID, data); err != nil {
			return location{}, err
		} else if ok {
			return loc, nil
		}
		// Active page didn't have room (or no longer exists in this Catalog's
		// bookkeeping) — fall through and allocate a fresh page below.
	}

	pageID, err := c.fm.AllocatePage()
	if err != nil {
		return location{}, fmt.Errorf("allocating new page: %w", err)
	}

	// pageID was just allocated by us, under activeMu, and cannot yet be referenced
	// by any existing location in the index (it has never held a record before), so
	// no other goroutine can be concurrently reading/mutating it via tombstone or
	// tryInsertInto at this point. The pageStripes lock is still taken here (rather
	// than skipped as an optimization) purely for defense-in-depth/consistency with
	// every other ReadPage/WritePage-around-mutation sequence in this file.
	pageStripe := pageStripeFor(pageID)
	c.pageStripes[pageStripe].Lock()
	defer c.pageStripes[pageStripe].Unlock()

	page := NewPage()
	slotID, err := page.InsertSlot(data)
	if err != nil {
		// A brand-new, empty page failing to hold a single record would indicate
		// data larger than PageSize, which Encode's fixed RecordEncodedSize should
		// never produce; surface the error rather than masking it.
		return location{}, fmt.Errorf("inserting into freshly allocated page %d: %w", pageID, err)
	}
	if err := c.fm.WritePage(pageID, page); err != nil {
		return location{}, fmt.Errorf("writing freshly allocated page %d: %w", pageID, err)
	}

	c.activePageID = pageID
	return location{pageID: pageID, slotID: slotID}, nil
}

// tryInsertInto attempts to insert data into the existing page pageID, returning
// ok=false (with no error) if the page does not have room, so the caller can fall
// back to allocating a new page.
func (c *Catalog) tryInsertInto(pageID uint64, data []byte) (location, bool, error) {
	pageStripe := pageStripeFor(pageID)
	c.pageStripes[pageStripe].Lock()
	defer c.pageStripes[pageStripe].Unlock()

	page, err := c.fm.ReadPage(pageID)
	if err != nil {
		return location{}, false, fmt.Errorf("reading active page %d: %w", pageID, err)
	}

	slotID, err := page.InsertSlot(data)
	if err != nil {
		// Not enough free space; not a real error for the caller, just "try elsewhere".
		return location{}, false, nil
	}

	if err := c.fm.WritePage(pageID, page); err != nil {
		return location{}, false, fmt.Errorf("writing active page %d: %w", pageID, err)
	}

	return location{pageID: pageID, slotID: slotID}, true, nil
}
