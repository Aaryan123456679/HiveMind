package catalog

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
)

// DefaultCatalogFileName is the conventional repo-relative path callers should use
// for the on-disk catalog file (see docs/LLD/catalog.md). Tests must not use this
// constant directly for I/O; they should use an isolated t.TempDir() path instead so
// parallel test runs and CI never collide or leave stray artifacts on disk.
const DefaultCatalogFileName = ".meta/catalog.dat"

// freeListPageID is the reserved page ID for the free-list bitmap page. Page 0 is
// never itself allocated as a data page; ReadPage/WritePage/FreePage all reject it.
const freeListPageID uint64 = 0

// Fixed byte offsets within the free-list bitmap page. All multi-byte integers are
// little-endian, matching record.go/page.go's on-disk encoding convention.
const (
	bitmapOffHighestAllocated = 0
	bitmapHeaderSize          = bitmapOffHighestAllocated + 8
	// bitmapBitsOffset is where the actual per-page-ID bitmap bytes begin.
	bitmapBitsOffset = bitmapHeaderSize
	// bitmapCapacityBits is the number of page IDs representable by a single bitmap
	// page. Bit i (0-indexed) tracks page ID (i+1), since page 0 is reserved for the
	// bitmap itself and is always considered unavailable for allocation.
	//
	// This is a hard capacity ceiling on the catalog file, not merely a soft
	// default: with PageSize=4096 and bitmapHeaderSize=8, bitmapCapacityBits
	// evaluates to (4096-8)*8 = 32704 pages, i.e. a maximum catalog.dat size of
	// (32704+1)*4096 bytes ≈ 128MB (the +1 accounts for page 0, the bitmap page
	// itself, which is not an allocatable data page). There is no chaining to
	// additional bitmap pages; once highestAllocated reaches bitmapCapacityBits
	// and no freed page is available for reuse, AllocatePage returns an explicit
	// "free-list exhausted" error (see AllocatePage below and
	// TestFreeListCapacityExhaustionSurfacesError in file_test.go) rather than
	// corrupting the bitmap or silently misallocating. This is a known, accepted
	// limitation for this phase (see regression.jsonl subtask 1.1.3 and
	// docs/LLD/catalog.md's "Free-list capacity ceiling" section) pending a
	// future multi-page/extensible free-list representation, needed only once a
	// single catalog file must track more than ~32.7k pages.
	bitmapCapacityBits = (PageSize - bitmapHeaderSize) * 8
)

// FileManager wraps an *os.File handle to a catalog data file (conventionally
// .meta/catalog.dat) made up of fixed PageSize-byte pages. Page 0 is a dedicated
// free-list page: a bitmap where bit i tracks whether page (i+1) is currently
// allocated (1) or free (0). This is the free-list encoding choice documented in
// docs/LLD/catalog.md ("free-list page reclaiming deleted/merged slots"); a bitmap
// was chosen over a linked list of free page IDs because it is simplest to persist
// atomically as a single fixed-size page for the page-count ranges this phase needs
// (see architecture-discovery.md for the full rationale and capacity numbers).
//
// FileManager has narrow internal locking (see mu below): it is safe for concurrent
// use by multiple goroutines out of the box, without requiring any external locking
// from callers. Only the genuinely shared, file-wide bookkeeping state
// (highestAllocated and the free-list bitmap) is guarded; the actual page I/O
// (pread/pwrite/fsync) is NOT serialized by FileManager, so concurrent
// ReadPage/WritePage calls to different, already-allocated pages proceed in
// parallel — this is safe because distinct pages occupy non-overlapping regions of
// the underlying file. FileManager also does not implement a write-ahead log;
// free-list mutations are made durable via a direct WriteAt+Sync of the bitmap page,
// which is sufficient for this subtask's acceptance criteria but is not a substitute
// for the WAL that engine/wal/ will provide for full catalog mutations.
type FileManager struct {
	file *os.File

	// mu guards ONLY the fields below (bitmap, highestAllocated) and the brief
	// bitmap-check/bitmap-mutation critical sections in AllocatePage, FreePage, and
	// validDataPageID's read of highestAllocated. It is deliberately NOT held around
	// the raw file.ReadAt/WriteAt/Sync syscalls in ReadPage/WritePage/persistBitmap's
	// disk I/O beyond what's needed to snapshot/mutate this bookkeeping state, so
	// concurrent operations on different pages/fileIDs are not serialized behind one
	// another's disk I/O. This is the fix for the over-broad caller-side fmMu lock
	// that catalog.go previously required: the lock now lives here, scoped to the
	// actual shared state, and callers need no locking of their own around
	// FileManager calls.
	mu sync.Mutex

	// bitmap holds the raw bytes of the free-list page (page 0), including its
	// header, mirrored in memory for fast lookups. It is kept in sync with the
	// on-disk copy by persistBitmap after every mutation. Guarded by mu.
	bitmap [PageSize]byte

	// highestAllocated is the highest page ID ever allocated (i.e. the current
	// file-extension high-water mark). Page IDs 1..highestAllocated are the only
	// ones that physically exist in the file; each is either used or free per the
	// bitmap. Guarded by mu.
	highestAllocated uint64

	// writeDelay, if non-nil, is invoked by WritePage immediately before it performs
	// its WriteAt+Sync I/O, and specifically AFTER validDataPageID's brief mu section
	// has already been released. It exists solely so tests (see file_test.go) can
	// simulate slow disk I/O deterministically, in order to prove that mu is not held
	// during that I/O (i.e. that other pages'/fileIDs' operations, and AllocatePage/
	// FreePage, are not serialized behind it). It is always nil in production use;
	// nothing in this package ever sets it outside of tests.
	writeDelay func()
}

// Open opens the catalog file at path, creating it (and its initial free-list page)
// if it does not already exist. If the file already exists, its free-list page is
// read back to restore in-memory free/used state.
func Open(path string) (*FileManager, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("catalog: open %s: %w", path, err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("catalog: stat %s: %w", path, err)
	}

	fm := &FileManager{file: file}

	if info.Size() == 0 {
		// Freshly created (or truncated-to-empty) file: initialize a brand-new,
		// all-free bitmap page as page 0 and persist it immediately.
		fm.highestAllocated = 0
		if err := fm.persistBitmapLocked(); err != nil {
			file.Close()
			return nil, fmt.Errorf("catalog: initializing free-list page for %s: %w", path, err)
		}
		return fm, nil
	}

	if info.Size()%PageSize != 0 {
		file.Close()
		return nil, fmt.Errorf("catalog: %s has invalid size %d bytes: not a multiple of PageSize (%d)", path, info.Size(), PageSize)
	}

	if _, err := file.ReadAt(fm.bitmap[:], 0); err != nil {
		file.Close()
		return nil, fmt.Errorf("catalog: reading free-list page from %s: %w", path, err)
	}
	fm.highestAllocated = binary.LittleEndian.Uint64(fm.bitmap[bitmapOffHighestAllocated:])

	wantPages := fm.highestAllocated + 1 // +1 for the bitmap page itself
	gotPages := uint64(info.Size()) / PageSize
	if gotPages != wantPages {
		file.Close()
		return nil, fmt.Errorf("catalog: %s is corrupt: free-list reports %d pages but file has %d", path, wantPages, gotPages)
	}

	return fm, nil
}

// Close closes the underlying file handle.
func (fm *FileManager) Close() error {
	return fm.file.Close()
}

// AllocatePage returns a free page ID, marking it used in the free-list. It prefers
// reusing a previously-freed page ID over extending the file; only when no free page
// exists does it extend the file by one page.
//
// Capacity is hard-capped at bitmapCapacityBits pages (see its doc comment for the
// exact numbers): once highestAllocated reaches that ceiling and no freed page is
// available for reuse, AllocatePage returns an explicit, documented error instead of
// an ambiguous failure (e.g. a corrupted bitmap write or a silently wrong page ID).
// Callers must treat this error as "catalog is at capacity", not as a transient I/O
// failure worth retrying.
//
// Known crash-window limitation (see regression.jsonl subtask 1.1.3 and
// docs/LLD/catalog.md's "Free-list capacity ceiling and crash-window reopen risk"
// section): extending the file (the WriteAt below) and persisting the updated
// bitmap+highestAllocated (persistBitmapLocked's own WriteAt+Sync) are two separate,
// non-atomic durable writes. If the process crashes after the file-extension WriteAt
// succeeds but before persistBitmapLocked's WriteAt+Sync completes, the file on disk
// is physically larger than what the persisted (stale) highestAllocated records. This
// is safe — Open's consistency check (gotPages != wantPages) detects the mismatch and
// hard-errors ("catalog is corrupt") rather than silently misreporting free/used
// state — but it means the catalog cannot be reopened at all until a future
// WAL-based re-derivation or repair pass lands; there is no automatic recovery today.
func (fm *FileManager) AllocatePage() (uint64, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	for id := uint64(1); id <= fm.highestAllocated; id++ {
		if !fm.isUsed(id) {
			fm.setUsed(id, true)
			if err := fm.persistBitmapLocked(); err != nil {
				return 0, err
			}
			return id, nil
		}
	}

	// No free page available: extend the file by one page.
	newID := fm.highestAllocated + 1
	if newID > bitmapCapacityBits {
		return 0, fmt.Errorf("catalog: free-list exhausted: cannot allocate beyond page %d bits in a single bitmap page", bitmapCapacityBits)
	}

	var zeroPage [PageSize]byte
	offset := int64(newID) * PageSize
	if _, err := fm.file.WriteAt(zeroPage[:], offset); err != nil {
		return 0, fmt.Errorf("catalog: extending file for new page %d: %w", newID, err)
	}

	fm.highestAllocated = newID
	fm.setUsed(newID, true)
	if err := fm.persistBitmapLocked(); err != nil {
		return 0, err
	}
	return newID, nil
}

// FreePage returns pageID to the free-list, making it eligible for reuse by a future
// AllocatePage call. This is the file-manager-level mechanism behind "deleting/
// merging a slot returns the page to free-list reclamation": callers that delete or
// merge catalog records such that a page becomes empty invoke FreePage directly.
//
// FreePage rejects a double-free: if pageID's bitmap bit is already clear (i.e. the
// page is already free), it returns an explicit error instead of silently
// succeeding. This is deliberate: a caller that erroneously calls FreePage twice on
// the same page ID it (wrongly) still thinks it owns is a real bug — most
// dangerously, in the window between the two calls another goroutine may have
// legitimately AllocatePage'd and reused that same page ID for something else, and a
// silent second free would let the erroneous caller re-free (and make eligible for a
// third, conflicting reallocation) a page that is now actively in use. Failing loudly
// here turns that bug pattern into an immediate error at the second call site.
func (fm *FileManager) FreePage(pageID uint64) error {
	if pageID == freeListPageID {
		return fmt.Errorf("catalog: cannot free reserved free-list page %d", pageID)
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()

	if pageID == 0 || pageID > fm.highestAllocated {
		return fmt.Errorf("catalog: cannot free page %d: not an allocated page (highest allocated is %d)", pageID, fm.highestAllocated)
	}

	if !fm.isUsed(pageID) {
		return fmt.Errorf("catalog: cannot free page %d: already free (double-free)", pageID)
	}

	fm.setUsed(pageID, false)
	return fm.persistBitmapLocked()
}

// ReadPage reads the page stored at pageID from disk.
func (fm *FileManager) ReadPage(pageID uint64) (*Page, error) {
	if err := fm.validDataPageID(pageID); err != nil {
		return nil, err
	}

	// Not covered by fm.mu, deliberately: see WritePage's comment below for why
	// concurrent I/O on distinct pages is safe without serialization.
	p := &Page{}
	offset := int64(pageID) * PageSize
	if _, err := fm.file.ReadAt(p.buf[:], offset); err != nil {
		return nil, fmt.Errorf("catalog: reading page %d: %w", pageID, err)
	}
	return p, nil
}

// WritePage writes p to disk at pageID's offset and durably persists it (WriteAt +
// Sync).
func (fm *FileManager) WritePage(pageID uint64, p *Page) error {
	if err := fm.validDataPageID(pageID); err != nil {
		return err
	}

	// The pread/pwrite/fsync below are intentionally NOT covered by fm.mu: distinct
	// pageIDs occupy non-overlapping byte ranges of the file, so concurrent I/O on
	// different pages is safe without synchronization, and serializing it would
	// reintroduce exactly the cross-fileID contention this locking model exists to
	// avoid (see the FileManager doc comment above).
	if fm.writeDelay != nil {
		fm.writeDelay()
	}

	offset := int64(pageID) * PageSize
	if _, err := fm.file.WriteAt(p.buf[:], offset); err != nil {
		return fmt.Errorf("catalog: writing page %d: %w", pageID, err)
	}
	if err := fm.file.Sync(); err != nil {
		return fmt.Errorf("catalog: syncing page %d: %w", pageID, err)
	}
	return nil
}

// validDataPageID reports an error if pageID is the reserved free-list page or does
// not refer to a page that currently exists in the file. It briefly holds fm.mu only
// to snapshot highestAllocated consistently; it does not perform any I/O.
func (fm *FileManager) validDataPageID(pageID uint64) error {
	if pageID == freeListPageID {
		return fmt.Errorf("catalog: page %d is the reserved free-list page, not a data page", pageID)
	}

	fm.mu.Lock()
	highest := fm.highestAllocated
	fm.mu.Unlock()

	if pageID > highest {
		return fmt.Errorf("catalog: page %d does not exist (highest allocated is %d)", pageID, highest)
	}
	return nil
}

// --- bitmap helpers ---
//
// Bit i (0-indexed) within the bitmap byte region tracks page ID (i+1). This offset
// exists because page 0 itself is reserved for the bitmap page and is never a valid
// allocation target.

func (fm *FileManager) bitIndex(pageID uint64) uint64 {
	return pageID - 1
}

func (fm *FileManager) isUsed(pageID uint64) bool {
	bit := fm.bitIndex(pageID)
	byteOff := bitmapBitsOffset + bit/8
	mask := byte(1) << (bit % 8)
	return fm.bitmap[byteOff]&mask != 0
}

func (fm *FileManager) setUsed(pageID uint64, used bool) {
	bit := fm.bitIndex(pageID)
	byteOff := bitmapBitsOffset + bit/8
	mask := byte(1) << (bit % 8)
	if used {
		fm.bitmap[byteOff] |= mask
	} else {
		fm.bitmap[byteOff] &^= mask
	}
}

// persistBitmapLocked writes the current in-memory highestAllocated + bitmap bytes to
// page 0 of the file, followed by an fsync, so the free-list survives process
// restarts. Callers must already hold fm.mu (or, as in Open, be certain no other
// goroutine yet has a reference to fm), since it reads/mutates the shared bitmap
// field in place.
func (fm *FileManager) persistBitmapLocked() error {
	binary.LittleEndian.PutUint64(fm.bitmap[bitmapOffHighestAllocated:], fm.highestAllocated)

	if _, err := fm.file.WriteAt(fm.bitmap[:], 0); err != nil {
		return fmt.Errorf("catalog: persisting free-list page: %w", err)
	}
	if err := fm.file.Sync(); err != nil {
		return fmt.Errorf("catalog: syncing free-list page: %w", err)
	}
	return nil
}
