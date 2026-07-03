package catalog

import (
	"encoding/binary"
	"fmt"
)

// PageSize is the fixed size, in bytes, of every catalog page. This is the exact
// number of bytes read/written to/from `.meta/catalog.dat` per page (see
// docs/LLD/catalog.md); a Page must always fit in exactly this many bytes.
const PageSize = 4096

// Page is a single fixed-size, 4KB slotted page: a Postgres/SQLite-style physical
// storage unit that holds a variable number of variable-length byte records ("slots").
//
// Layout (classic slotted-page design):
//
//	byte 0                                                              byte PageSize-1
//	+------------------+------------------+-------------------+------------------+
//	|   page header    |   slot directory  |    free space     |   tuple data     |
//	| (pageHeaderSize)  |  (grows downward, |                   | (grows upward,   |
//	|                   |   one entry per   |                   |  from the end    |
//	|                   |   slot, appended  |                   |  of the page     |
//	|                   |   after header)   |                   |  toward freeStart)|
//	+------------------+------------------+-------------------+------------------+
//	                    ^ freeStart                             ^ freeEnd
//
// The page header records slotCount (number of slot directory entries, including
// tombstoned/deleted ones -- slot IDs are never reused so existing ReadSlot/DeleteSlot
// callers keep valid IDs), freeStart (the offset of the next free byte in the slot
// directory region, i.e. where a new slot directory entry would be appended), and
// freeEnd (the offset of the start of the lowest-allocated tuple region, i.e. where
// the next tuple's bytes would be placed, growing downward toward freeStart). Free
// space available for a brand-new slot is exactly freeEnd - freeStart.
//
// Each slot directory entry is a fixed 8 bytes: {offset uint16, length uint16,
// deleted uint16 (0 or 1), reserved uint16}. offset/length describe where the slot's
// tuple bytes live in the tuple-data region; deleted marks a tombstoned slot whose
// directory entry is retained (so its slotID stays stable) but whose data is no
// longer considered live.
//
// Deletion in this subtask is tombstone-only: DeleteSlot does not compact or move any
// tuple bytes. Reclaiming a deleted slot's space happens only via InsertSlot's reuse
// path (see InsertSlot), which finds a tombstoned slot with enough capacity and
// overwrites its tuple bytes in place. Full in-page compaction/defragmentation (e.g.
// to reclaim space from a deleted slot for a *larger* subsequent insert) is a
// documented future improvement, not required by this subtask's acceptance criteria.
//
// Page has no internal locking. It is safe to use in the single-threaded storage-core
// phase this subtask targets; a future subtask (striped-mutex CRUD, see
// docs/LLD/catalog.md) is responsible for synchronizing concurrent access to a shared
// Page from multiple goroutines.
type Page struct {
	buf [PageSize]byte
}

// Fixed byte offsets/widths for the page header. All multi-byte integers are
// little-endian, matching record.go's on-disk encoding convention.
const (
	offSlotCount = 0
	offFreeStart = offSlotCount + 2
	offFreeEnd   = offFreeStart + 2

	// pageHeaderSize is the fixed size in bytes of the page header.
	pageHeaderSize = offFreeEnd + 2
)

// Fixed byte offsets/widths for a single slot directory entry.
const (
	slotOffOffset   = 0
	slotOffLength   = slotOffOffset + 2
	slotOffDeleted  = slotOffLength + 2
	slotOffReserved = slotOffDeleted + 2

	// slotHeaderSize is the fixed size in bytes of one slot directory entry.
	slotHeaderSize = slotOffReserved + 2
)

// NewPage returns a freshly initialized, empty Page with no slots and the full page
// (minus the header) available as free space.
func NewPage() *Page {
	p := &Page{}
	p.setSlotCount(0)
	p.setFreeStart(pageHeaderSize)
	p.setFreeEnd(PageSize)
	return p
}

// --- page header accessors ---

func (p *Page) slotCount() int {
	return int(binary.LittleEndian.Uint16(p.buf[offSlotCount:]))
}

func (p *Page) setSlotCount(n int) {
	binary.LittleEndian.PutUint16(p.buf[offSlotCount:], uint16(n))
}

func (p *Page) freeStart() int {
	return int(binary.LittleEndian.Uint16(p.buf[offFreeStart:]))
}

func (p *Page) setFreeStart(v int) {
	binary.LittleEndian.PutUint16(p.buf[offFreeStart:], uint16(v))
}

func (p *Page) freeEnd() int {
	return int(binary.LittleEndian.Uint16(p.buf[offFreeEnd:]))
}

func (p *Page) setFreeEnd(v int) {
	binary.LittleEndian.PutUint16(p.buf[offFreeEnd:], uint16(v))
}

// --- slot directory entry accessors ---

func (p *Page) slotEntryOffset(slotID int) int {
	return pageHeaderSize + slotID*slotHeaderSize
}

func (p *Page) slotDataOffset(slotID int) int {
	o := p.slotEntryOffset(slotID)
	return int(binary.LittleEndian.Uint16(p.buf[o+slotOffOffset:]))
}

func (p *Page) setSlotDataOffset(slotID int, v int) {
	o := p.slotEntryOffset(slotID)
	binary.LittleEndian.PutUint16(p.buf[o+slotOffOffset:], uint16(v))
}

func (p *Page) slotDataLength(slotID int) int {
	o := p.slotEntryOffset(slotID)
	return int(binary.LittleEndian.Uint16(p.buf[o+slotOffLength:]))
}

func (p *Page) setSlotDataLength(slotID int, v int) {
	o := p.slotEntryOffset(slotID)
	binary.LittleEndian.PutUint16(p.buf[o+slotOffLength:], uint16(v))
}

func (p *Page) slotDeleted(slotID int) bool {
	o := p.slotEntryOffset(slotID)
	return binary.LittleEndian.Uint16(p.buf[o+slotOffDeleted:]) != 0
}

func (p *Page) setSlotDeleted(slotID int, deleted bool) {
	o := p.slotEntryOffset(slotID)
	var v uint16
	if deleted {
		v = 1
	}
	binary.LittleEndian.PutUint16(p.buf[o+slotOffDeleted:], v)
}

// FreeSpace returns the number of bytes currently available in the page for a new
// slot directory entry + tuple data (i.e. what InsertSlot needs, in the worst case
// where no tombstoned slot can be reused).
func (p *Page) FreeSpace() int {
	return p.freeEnd() - p.freeStart()
}

// SlotCount returns the total number of slot directory entries in the page, including
// tombstoned/deleted ones.
func (p *Page) SlotCount() int {
	return p.slotCount()
}

// validSlotID reports whether slotID refers to an existing slot directory entry
// (regardless of whether it has been deleted).
func (p *Page) validSlotID(slotID int) bool {
	return slotID >= 0 && slotID < p.slotCount()
}

// InsertSlot stores data as a new slot in the page and returns its slotID. If an
// existing tombstoned (deleted) slot has enough reserved capacity to hold data, that
// slot's space is reused in place (no new slot directory entry or tuple-region bytes
// are allocated) and its slotID is returned. Otherwise a brand new slot directory
// entry and tuple region are appended, provided the page has enough free space for
// both; if not, InsertSlot returns a non-nil error rather than truncating data or
// panicking.
func (p *Page) InsertSlot(data []byte) (int, error) {
	if len(data) > PageSize {
		return 0, fmt.Errorf("catalog: page: slot data of %d bytes exceeds page size %d", len(data), PageSize)
	}

	// First, try to reuse a tombstoned slot with enough reserved capacity.
	for slotID := 0; slotID < p.slotCount(); slotID++ {
		if p.slotDeleted(slotID) && p.slotDataLength(slotID) >= len(data) {
			off := p.slotDataOffset(slotID)
			copy(p.buf[off:off+len(data)], data)
			p.setSlotDataLength(slotID, len(data))
			p.setSlotDeleted(slotID, false)
			return slotID, nil
		}
	}

	// No reusable tombstoned slot; allocate a new directory entry + tuple bytes.
	needed := slotHeaderSize + len(data)
	if needed > p.FreeSpace() {
		return 0, fmt.Errorf("catalog: page: insert of %d bytes would overflow page: need %d bytes (slot header + data), have %d bytes free", len(data), needed, p.FreeSpace())
	}

	slotID := p.slotCount()
	newFreeEnd := p.freeEnd() - len(data)
	copy(p.buf[newFreeEnd:p.freeEnd()], data)
	p.setFreeEnd(newFreeEnd)

	p.setSlotDataOffset(slotID, newFreeEnd)
	p.setSlotDataLength(slotID, len(data))
	p.setSlotDeleted(slotID, false)

	p.setFreeStart(p.freeStart() + slotHeaderSize)
	p.setSlotCount(slotID + 1)

	return slotID, nil
}

// ReadSlot returns a copy of the bytes stored at slotID. It returns an error if
// slotID does not refer to an existing slot, or if that slot has been deleted.
func (p *Page) ReadSlot(slotID int) ([]byte, error) {
	if !p.validSlotID(slotID) {
		return nil, fmt.Errorf("catalog: page: slot %d does not exist (slot count %d)", slotID, p.slotCount())
	}
	if p.slotDeleted(slotID) {
		return nil, fmt.Errorf("catalog: page: slot %d has been deleted", slotID)
	}

	off := p.slotDataOffset(slotID)
	length := p.slotDataLength(slotID)
	out := make([]byte, length)
	copy(out, p.buf[off:off+length])
	return out, nil
}

// DeleteSlot tombstones slotID: the slot directory entry is retained (so the slotID
// stays stable and out-of-range checks keep working) but the slot is marked deleted,
// and future ReadSlot calls on it will error. The slot's reserved tuple-region
// capacity becomes eligible for reuse by a subsequent InsertSlot whose data fits
// within that capacity (see InsertSlot). This subtask does not perform full in-page
// compaction/defragmentation.
func (p *Page) DeleteSlot(slotID int) error {
	if !p.validSlotID(slotID) {
		return fmt.Errorf("catalog: page: slot %d does not exist (slot count %d)", slotID, p.slotCount())
	}
	if p.slotDeleted(slotID) {
		return fmt.Errorf("catalog: page: slot %d already deleted", slotID)
	}

	p.setSlotDeleted(slotID, true)
	return nil
}
