package catalog

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
)

// InvalidFileID is the reserved sentinel value for an unset/invalid fileID. Real
// fileIDs handed out by IDAllocator.Next always start at 1 and increase strictly
// from there (see docs/LLD/catalog.md: "fileID allocation is a monotonically
// increasing atomic counter — no reuse, no gaps-matter semantics").
const InvalidFileID uint64 = 0

// idAllocSuffix names the small sidecar file, kept alongside the main catalog data
// file, that durably persists the ID allocator's high-water-mark (the highest
// fileID ever handed out). See the "Design decision" section of
// architecture-discovery.md for subtask 1.1.4 for the rationale behind a sidecar
// file rather than extending FileManager's page-0 free-list bitmap or borrowing a
// regular data page:
//
//   - Page 0's bitmap header is only 8 bytes; the entire remainder of page 0 is
//     already consumed by the free-list bitmap bits themselves (bitmapCapacityBits
//     spans every remaining byte), so there is no meaningfully "free" padding to
//     reuse there without shrinking the free-list's addressable page-ID space or
//     touching file.go's on-disk layout/durability code that subtask 1.1.3 already
//     implemented and had independently verified.
//   - Borrowing a fixed regular data page (e.g. always page 1) would only be
//     guaranteed to hold this allocator's state if IDAllocator were always the very
//     first caller of FileManager.AllocatePage for the lifetime of the system — a
//     fragile, implicit ordering dependency with no way to rediscover the page ID
//     on a later reopen without already knowing it up front.
//
// Instead, IDAllocator manages its own tiny (idAllocStateSize-byte) file, deriving
// its path deterministically from the catalog file's own path, and durably persists
// to it using the exact same WriteAt+Sync pattern FileManager itself relies on for
// its bitmap page. This keeps 1.1.4 fully isolated from file.go/page.go: it touches
// neither the free-list bitmap format nor the regular data-page allocation space.
const idAllocSuffix = ".idalloc"

// idAllocStateSize is the fixed size, in bytes, of the sidecar file's contents: a
// single little-endian uint64 high-water-mark (the highest fileID ever allocated,
// or 0 for a fresh/never-allocated-from catalog).
const idAllocStateSize = 8

// IDAllocator hands out monotonically increasing fileIDs for use as
// CatalogRecord.FileID values. It never reuses an ID, even after the catalog record
// referencing it has been deleted (unlike FileManager's page free-list, whose
// gaps/reuse semantics do not apply here — see docs/LLD/catalog.md). The zero value
// is not a valid IDAllocator; use NewIDAllocator.
//
// IDAllocator is safe for concurrent use by multiple goroutines: Next() serializes
// "increment in memory + durably persist the new high-water-mark" as a single
// critical section under a mutex. A plain mutex (rather than a lock-free
// atomic.Uint64) is used deliberately here because every call must synchronously
// fsync a durable write before returning, so the two operations need to happen
// together anyway; this is a narrow, single-purpose allocator lock, not the
// catalog's striped-mutex CRUD design (a later subtask) and does not conflict with
// docs/LLD/catalog.md's "no single global lock" principle, which applies to the
// catalog record CRUD API.
type IDAllocator struct {
	mu sync.Mutex

	// next is the highest fileID allocated so far (0 if none have been allocated
	// yet from a fresh catalog). The next call to Next() will hand out next+1.
	next uint64

	// stateFile is the open sidecar file backing durable persistence of next.
	stateFile *os.File
}

// NewIDAllocator opens (creating if necessary) the sidecar state file alongside
// fm's underlying catalog file, and restores the in-memory high-water-mark from
// whatever was last durably persisted there (0 for a brand-new catalog, so the
// first Next() call returns 1).
func NewIDAllocator(fm *FileManager) (*IDAllocator, error) {
	path := fm.file.Name() + idAllocSuffix

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("catalog: idalloc: open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("catalog: idalloc: stat %s: %w", path, err)
	}

	var next uint64
	switch info.Size() {
	case 0:
		// Freshly created sidecar file: no fileID has ever been allocated from this
		// catalog. next stays 0, so the first Next() call returns 1.
	case idAllocStateSize:
		var buf [idAllocStateSize]byte
		if _, err := f.ReadAt(buf[:], 0); err != nil {
			f.Close()
			return nil, fmt.Errorf("catalog: idalloc: reading state from %s: %w", path, err)
		}
		next = binary.LittleEndian.Uint64(buf[:])
	default:
		f.Close()
		return nil, fmt.Errorf("catalog: idalloc: %s has invalid size %d bytes: want 0 (fresh) or %d", path, info.Size(), idAllocStateSize)
	}

	return &IDAllocator{next: next, stateFile: f}, nil
}

// Next atomically allocates and returns the next fileID: strictly greater than
// every fileID previously returned by this IDAllocator (across the lifetime of the
// underlying catalog file, including prior process runs), starting from 1 (0 is
// reserved, see InvalidFileID). The new high-water-mark is durably persisted
// (WriteAt + Sync) before Next returns successfully, so a subsequent reopen of the
// same catalog file will never hand out a colliding or reused fileID.
//
// If the durable persist fails, Next returns a non-nil error and does not advance
// the in-memory counter, so the allocator's in-memory state never gets ahead of
// what has actually been made durable.
func (a *IDAllocator) Next() (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	candidate := a.next + 1

	var buf [idAllocStateSize]byte
	binary.LittleEndian.PutUint64(buf[:], candidate)
	if _, err := a.stateFile.WriteAt(buf[:], 0); err != nil {
		return 0, fmt.Errorf("catalog: idalloc: persisting fileID high-water-mark %d: %w", candidate, err)
	}
	if err := a.stateFile.Sync(); err != nil {
		return 0, fmt.Errorf("catalog: idalloc: syncing fileID high-water-mark %d: %w", candidate, err)
	}

	a.next = candidate
	return candidate, nil
}

// Close closes the allocator's underlying sidecar file handle.
func (a *IDAllocator) Close() error {
	return a.stateFile.Close()
}
