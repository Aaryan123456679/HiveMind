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

// TestInsertSlotShrinkReuseCapacity pins down InsertSlot's documented shrink-reuse
// capacity behavior (see the InsertSlot doc comment and regression.jsonl subtask
// 1.1.2): a tombstoned slot's reuse-eligibility capacity is tracked via the length of
// the most recently written data, not the slot's original tuple-region footprint. Once
// a slot has been reused with a smaller payload, its capacity is permanently shrunk to
// that smaller size for all future reuse decisions -- it never grows back to the
// original, larger footprint, even though the original tuple-region bytes are never
// physically reclaimed.
//
// Sequence: insert N bytes, delete, reinsert M<N bytes (reusing the same slot, now
// tracking capacity M), delete again, then attempt to reinsert a size strictly between
// M and N. That reinsert must NOT reuse the shrunk slot (because size > M) and, since
// the page is otherwise completely full, must fail with an overflow error rather than
// silently succeeding via the slot's stale, larger original capacity of N.
func TestInsertSlotShrinkReuseCapacity(t *testing.T) {
	p := NewPage()

	const n = 100      // N: original payload size.
	const m = 40       // M: shrunk reinsert size, M < N.
	const between = 70 // a size strictly between M and N.

	nBytes := bytes.Repeat([]byte{0x11}, n)
	mBytes := bytes.Repeat([]byte{0x22}, m)
	betweenBytes := bytes.Repeat([]byte{0x33}, between)

	// Fill the page completely with N-byte payloads so there is no free space left
	// beyond what individual slot deletions reclaim; this removes any ambiguity that
	// a later reinsert could succeed via space *other* than the reused slot.
	var lastSlotID int
	for {
		id, err := p.InsertSlot(nBytes)
		if err != nil {
			break
		}
		lastSlotID = id
	}
	if _, err := p.InsertSlot(nBytes); err == nil {
		t.Fatal("expected page to be full before deleting a slot")
	}

	// Top off any remaining fresh free space (too small to fit another N-byte
	// payload, but potentially large enough to fit a "between"-sized payload via a
	// brand-new slot) with 1-byte filler slots, so that after the reused slot is
	// deleted the *only* free capacity in the page is what that single slot offers.
	// Without this, a later "between"-sized insert could spuriously succeed via this
	// leftover fresh space rather than actually exercising slot-capacity reuse.
	for {
		if _, err := p.InsertSlot([]byte{0x00}); err != nil {
			break
		}
	}

	// Delete the most recently inserted N-byte slot, freeing its capacity (currently
	// tracked as N).
	if err := p.DeleteSlot(lastSlotID); err != nil {
		t.Fatalf("DeleteSlot() (first delete) returned unexpected error: %v", err)
	}

	// Reinsert M<N bytes. This must reuse the same slot (proving the slot's original
	// N-byte capacity was available), and the slot's tracked capacity shrinks to M.
	mSlotID, err := p.InsertSlot(mBytes)
	if err != nil {
		t.Fatalf("InsertSlot(M bytes) after first delete: expected reuse to succeed, got error: %v", err)
	}
	if mSlotID != lastSlotID {
		t.Fatalf("InsertSlot(M bytes) after first delete: slotID = %d, want reused slotID %d", mSlotID, lastSlotID)
	}

	// Delete that slot again, now tombstoned with a tracked capacity of only M.
	if err := p.DeleteSlot(mSlotID); err != nil {
		t.Fatalf("DeleteSlot() (second delete) returned unexpected error: %v", err)
	}

	// Attempt to reinsert a size strictly between M and N. Per the documented
	// shrink-reuse behavior, the slot's tracked capacity is now M (not N), so this
	// insert must NOT reuse the slot via its stale original N-byte footprint. Since
	// the page has no other free space, this insert must fail outright.
	if _, err := p.InsertSlot(betweenBytes); err == nil {
		t.Fatal("InsertSlot(size between M and N) after shrink-reuse: expected overflow error (capacity should be pinned at shrunk M, not stale N), got nil")
	}

	// Confirm the shrunk slot's actual capacity (M bytes) is still reusable: a
	// reinsert of exactly M bytes must succeed and reuse the same slot.
	finalID, err := p.InsertSlot(mBytes)
	if err != nil {
		t.Fatalf("InsertSlot(M bytes) after shrink-reuse: expected reuse at shrunk capacity M to succeed, got error: %v", err)
	}
	if finalID != mSlotID {
		t.Fatalf("InsertSlot(M bytes) after shrink-reuse: slotID = %d, want reused slotID %d", finalID, mSlotID)
	}
}
