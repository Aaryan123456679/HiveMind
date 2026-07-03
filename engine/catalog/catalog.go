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
//  3. fmMu sync.Mutex, serializing every call into the shared *FileManager (fm.go's
//     AllocatePage/FreePage/ReadPage/WritePage). This is a necessary, deliberate
//     departure from pure per-fileID striping: FileManager (1.1.3) is explicitly
//     documented as having "no internal locking... a future subtask (striped-mutex
//     CRUD) is responsible for synchronizing concurrent access to a shared
//     FileManager" — and its internal state (highestAllocated, the free-list bitmap)
//     is genuinely shared, file-wide state, not state scoped to any one page or
//     fileID. Two different fileIDs' operations can legitimately touch the very same
//     physical page (many records fit per 4KB page) or mutate the very same
//     highestAllocated/bitmap fields (via AllocatePage/FreePage), so per-fileID
//     stripes alone cannot safely guard FileManager's own internals — a genuine
//     torn-read/data-race was observed under `go test -race` during this subtask's
//     development when only per-fileID stripes were used, confirming this is required
//     for correctness, not just caution. fmMu critical sections are kept as short as
//     possible (wrapping only the direct FileManager call, never a whole Put/Get/
//     Delete), so this does not reintroduce a single global lock over the CRUD API's
//     own logic (encode/decode, index lookups still use their own, finer-grained
//     locks); it does mean actual disk I/O throughput is currently bounded by
//     FileManager's lack of internal concurrency support, which is an accurate
//     reflection of 1.1.3's documented single-threaded design, not a defect
//     introduced by this subtask. A future optimization (once FileManager itself
//     gains finer-grained internal locking, e.g. per-page or per-bitmap-region) could
//     shrink or remove this lock's scope; not needed for 1.1.5's acceptance criteria.
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

	indexMu sync.RWMutex
	index   map[uint64]location

	// activeMu guards activePageID: the single shared "current page being appended
	// into for new inserts" cursor. A single shared active page (rather than one per
	// stripe) is a deliberate simplicity choice for this subtask; per-stripe active
	// pages would reduce contention further but are an optimization left for later,
	// once real throughput needs justify the added bookkeeping complexity.
	activeMu     sync.Mutex
	activePageID uint64 // 0 means "no active page allocated yet this process"

	// fmMu serializes every call into fm (see locking model item 3 above).
	fmMu sync.Mutex
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

// readSlot reads and returns the raw encoded bytes stored at loc, serialized against
// concurrent FileManager access via fmMu (see the Catalog doc comment, locking model
// item 3).
func (c *Catalog) readSlot(loc location) ([]byte, error) {
	c.fmMu.Lock()
	defer c.fmMu.Unlock()

	page, err := c.fm.ReadPage(loc.pageID)
	if err != nil {
		return nil, err
	}
	return page.ReadSlot(loc.slotID)
}

// tombstone deletes the slot at loc and durably writes the page back, serialized
// against concurrent FileManager access via fmMu.
func (c *Catalog) tombstone(loc location) error {
	c.fmMu.Lock()
	defer c.fmMu.Unlock()

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
// concurrent inserts' view of "which page is currently active"; fmMu (taken by the
// tryInsertInto/allocate-new-page helpers below) separately serializes the actual
// FileManager calls, per the Catalog doc comment's locking model item 3.
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

	c.fmMu.Lock()
	pageID, err := c.fm.AllocatePage()
	if err != nil {
		c.fmMu.Unlock()
		return location{}, fmt.Errorf("allocating new page: %w", err)
	}

	page := NewPage()
	slotID, err := page.InsertSlot(data)
	if err != nil {
		c.fmMu.Unlock()
		// A brand-new, empty page failing to hold a single record would indicate
		// data larger than PageSize, which Encode's fixed RecordEncodedSize should
		// never produce; surface the error rather than masking it.
		return location{}, fmt.Errorf("inserting into freshly allocated page %d: %w", pageID, err)
	}
	if err := c.fm.WritePage(pageID, page); err != nil {
		c.fmMu.Unlock()
		return location{}, fmt.Errorf("writing freshly allocated page %d: %w", pageID, err)
	}
	c.fmMu.Unlock()

	c.activePageID = pageID
	return location{pageID: pageID, slotID: slotID}, nil
}

// tryInsertInto attempts to insert data into the existing page pageID, returning
// ok=false (with no error) if the page does not have room, so the caller can fall
// back to allocating a new page.
func (c *Catalog) tryInsertInto(pageID uint64, data []byte) (location, bool, error) {
	c.fmMu.Lock()
	defer c.fmMu.Unlock()

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
