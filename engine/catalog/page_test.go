package catalog

import (
	"bytes"
	"testing"
)

// TestSlottedPageInsertReadDelete exercises the basic InsertSlot/ReadSlot/DeleteSlot
// happy path and free-space bookkeeping.
func TestSlottedPageInsertReadDelete(t *testing.T) {
	p := NewPage()

	initialFree := p.FreeSpace()
	if initialFree != PageSize-pageHeaderSize {
		t.Fatalf("initial FreeSpace() = %d, want %d", initialFree, PageSize-pageHeaderSize)
	}

	data := []byte("hello, catalog page")
	slotID, err := p.InsertSlot(data)
	if err != nil {
		t.Fatalf("InsertSlot() returned unexpected error: %v", err)
	}
	if slotID != 0 {
		t.Fatalf("InsertSlot() slotID = %d, want 0", slotID)
	}

	wantFree := initialFree - slotHeaderSize - len(data)
	if got := p.FreeSpace(); got != wantFree {
		t.Fatalf("FreeSpace() after insert = %d, want %d", got, wantFree)
	}

	got, err := p.ReadSlot(slotID)
	if err != nil {
		t.Fatalf("ReadSlot() returned unexpected error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("ReadSlot() = %q, want %q", got, data)
	}

	// Insert a second slot to make sure slot IDs and offsets don't collide.
	data2 := []byte("a second record")
	slotID2, err := p.InsertSlot(data2)
	if err != nil {
		t.Fatalf("InsertSlot() (2nd) returned unexpected error: %v", err)
	}
	if slotID2 != 1 {
		t.Fatalf("InsertSlot() (2nd) slotID = %d, want 1", slotID2)
	}

	got2, err := p.ReadSlot(slotID2)
	if err != nil {
		t.Fatalf("ReadSlot() (2nd) returned unexpected error: %v", err)
	}
	if !bytes.Equal(got2, data2) {
		t.Fatalf("ReadSlot() (2nd) = %q, want %q", got2, data2)
	}

	// The first slot's data must still be intact.
	got, err = p.ReadSlot(slotID)
	if err != nil {
		t.Fatalf("ReadSlot() (1st, re-read) returned unexpected error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("ReadSlot() (1st, re-read) = %q, want %q", got, data)
	}

	if err := p.DeleteSlot(slotID); err != nil {
		t.Fatalf("DeleteSlot() returned unexpected error: %v", err)
	}

	if _, err := p.ReadSlot(slotID); err == nil {
		t.Fatal("ReadSlot() after DeleteSlot(): expected error, got nil")
	}

	// Deleting an already-deleted slot must error, not panic.
	if err := p.DeleteSlot(slotID); err == nil {
		t.Fatal("DeleteSlot() on already-deleted slot: expected error, got nil")
	}

	// Deleting/reading an out-of-range slotID must error, not panic.
	if _, err := p.ReadSlot(999); err == nil {
		t.Fatal("ReadSlot() with out-of-range slotID: expected error, got nil")
	}
	if err := p.DeleteSlot(999); err == nil {
		t.Fatal("DeleteSlot() with out-of-range slotID: expected error, got nil")
	}
	if _, err := p.ReadSlot(-1); err == nil {
		t.Fatal("ReadSlot() with negative slotID: expected error, got nil")
	}
}

// TestSlottedPageOverflow inserts fixed-size records into a page until it is full,
// then asserts the next insert returns a non-nil overflow error rather than
// panicking or silently truncating data.
func TestSlottedPageOverflow(t *testing.T) {
	p := NewPage()

	payload := bytes.Repeat([]byte{0xAB}, 100)

	inserted := 0
	for {
		_, err := p.InsertSlot(payload)
		if err != nil {
			break
		}
		inserted++
		if inserted > PageSize { // safety valve against an infinite loop bug
			t.Fatal("InsertSlot() never returned an overflow error; page should have filled up")
		}
	}

	if inserted == 0 {
		t.Fatal("expected at least one successful insert before the page filled up")
	}

	// The page must now consistently reject further inserts of the same size.
	if _, err := p.InsertSlot(payload); err == nil {
		t.Fatal("InsertSlot() on a full page: expected overflow error, got nil")
	}

	// A tiny insert may still fail too, once truly no room remains for a new slot
	// header at all; but an insert that clearly cannot fit (larger than any
	// remaining free space) must definitely error.
	tooBig := bytes.Repeat([]byte{0xCD}, PageSize)
	if _, err := p.InsertSlot(tooBig); err == nil {
		t.Fatal("InsertSlot() with data larger than the page: expected error, got nil")
	}
}

// TestSlottedPageReuseAfterDelete fills a page to capacity, deletes one slot, and
// confirms that a subsequent insert of similar size actually reuses the freed slot's
// reclaimed space (not just that the operation reports success). It does this by
// first proving a fresh-space insert of that size is impossible (the page is full),
// then showing the same-sized insert succeeds immediately after a delete, and that
// the returned slotID is the deleted slot's ID -- which is only possible via the
// reuse path, since a brand-new slot would get the next unused (higher) slotID.
func TestSlottedPageReuseAfterDelete(t *testing.T) {
	p := NewPage()

	payload := bytes.Repeat([]byte{0x42}, 100)

	var lastSlotID int
	slotCountBefore := 0
	for {
		id, err := p.InsertSlot(payload)
		if err != nil {
			break
		}
		lastSlotID = id
		slotCountBefore = p.SlotCount()
	}

	// Confirm the page is indeed full: a fresh insert of the same size fails.
	if _, err := p.InsertSlot(payload); err == nil {
		t.Fatal("expected page to be full before deleting a slot")
	}

	// Delete the most recently inserted slot to free its reserved capacity.
	if err := p.DeleteSlot(lastSlotID); err != nil {
		t.Fatalf("DeleteSlot() returned unexpected error: %v", err)
	}

	// Reinsert data that fits within the freed slot's capacity. If free space were
	// not reclaimed from the deleted slot, this would fail exactly like the insert
	// above did (the page was already full).
	reused := bytes.Repeat([]byte{0x99}, 100)
	newID, err := p.InsertSlot(reused)
	if err != nil {
		t.Fatalf("InsertSlot() after DeleteSlot(): expected reuse to succeed, got error: %v", err)
	}

	if newID != lastSlotID {
		t.Fatalf("InsertSlot() after DeleteSlot(): slotID = %d, want reused slotID %d (proves space was NOT reused via the tombstoned slot)", newID, lastSlotID)
	}

	if got := p.SlotCount(); got != slotCountBefore {
		t.Fatalf("SlotCount() after reuse insert = %d, want unchanged %d (no new slot directory entry should have been allocated)", got, slotCountBefore)
	}

	got, err := p.ReadSlot(newID)
	if err != nil {
		t.Fatalf("ReadSlot() after reuse insert returned unexpected error: %v", err)
	}
	if !bytes.Equal(got, reused) {
		t.Fatalf("ReadSlot() after reuse insert = %q, want %q", got, reused)
	}

	// A reinsert that would have required a brand-new slot directory entry + fresh
	// tuple bytes (rather than reuse) must still fail, since the page had no other
	// free space beyond what was reclaimed from the deleted slot.
	if _, err := p.InsertSlot(payload); err == nil {
		t.Fatal("InsertSlot() beyond reclaimed capacity: expected overflow error, got nil")
	}
}
