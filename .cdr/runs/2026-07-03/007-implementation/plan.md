# Plan — Subtask 1.1.2

1. Create `engine/catalog/page.go`:
   - `const PageSize = 4096`
   - `slotHeaderSize` constant (8 bytes: offset uint16, length uint16, flags uint16,
     reserved uint16).
   - `pageHeaderSize` constant (e.g. 6 bytes: slotCount uint16, freeStart uint16,
     freeEnd uint16).
   - `type Page struct { buf [PageSize]byte }` with a constructor `NewPage() *Page`
     that zero-initializes header fields (slotCount=0, freeStart=pageHeaderSize,
     freeEnd=PageSize).
   - Internal helpers to read/write the header and a given slot directory entry via
     `encoding/binary` (little-endian, matching record.go's convention).
   - `func (p *Page) InsertSlot(data []byte) (int, error)`:
     - Reject empty/nil data? (allow zero-length data, but guard len(data) fits
       uint16, i.e. <= 65535 -- always true since PageSize=4096 anyway).
     - First scan existing tombstoned slots for one whose length >= len(data);
       if found, overwrite that slot's tuple region bytes in place, clear the
       deleted flag, update its stored length to len(data), return that slotID.
     - Else, compute needed = slotHeaderSize (new directory entry) + len(data)
       (new tuple bytes); if needed > FreeSpace(), return a descriptive overflow
       error (no panic). Else append new directory entry at freeStart, write tuple
       bytes ending at freeEnd (freeEnd -= len(data)), advance freeStart by
       slotHeaderSize, slotCount++, return new slotID.
   - `func (p *Page) ReadSlot(slotID int) ([]byte, error)`:
     - Validate 0 <= slotID < slotCount, else error.
     - Validate slot not tombstoned, else error.
     - Return a copy of buf[offset : offset+length].
   - `func (p *Page) DeleteSlot(slotID int) error`:
     - Validate slotID range + not already deleted.
     - Set deleted flag in directory entry. Do not touch freeStart/freeEnd (no
       compaction) -- tombstoned space is reusable only via the InsertSlot reuse
       path above, documented as a known limitation / future compaction TODO.
   - `func (p *Page) FreeSpace() int`: `int(freeEnd) - int(freeStart)`.
   - Doc-comment the whole layout at the top of the file (header/slot-array/tuple
     region diagram, matching the architecture-discovery.md description).

2. Create `engine/catalog/page_test.go`:
   - `TestSlottedPageInsertReadDelete` -- basic insert/read/delete/read-after-delete
     error happy path.
   - `TestSlottedPageOverflow` -- loop InsertSlot with fixed-size payloads until an
     error is returned; assert the error is non-nil and that the page did not panic;
     assert a reasonable number of slots were inserted before overflow (>0).
   - `TestSlottedPageFreeSpaceReuseAfterDelete` -- fill the page to (near) capacity,
     `DeleteSlot` one entry, then `InsertSlot` new data sized to fit in the freed
     slot's reclaimed space but NOT in whatever raw FreeSpace() remained beforehand
     (i.e. deliberately drive the page to a state where a fresh append would fail
     but the freed-slot reuse path succeeds) -- and assert the reinsert succeeds and,
     ideally, that the returned slotID equals the deleted slot's ID (proving reuse,
     not just success by coincidence of leftover raw free space).
   - Use `t.Run` subtests, name the top-level test file's primary test
     `TestSlottedPage` (per test-spec's `-run TestSlottedPage` filter) or use a
     `TestSlottedPage` parent test with subtests via t.Run so `-run TestSlottedPage`
     matches all of them (Go's -run matches on prefix through the / hierarchy).

3. Run `go build ./engine/...`, `go test ./engine/catalog/... -run TestSlottedPage
   -race -v`, and the full-package `go test ./engine/catalog/... -race -v`.

4. Write `validation-matrix.json`, `self-consistency.json`, commit, `handoff.json`,
   update `.cdr/index/file.jsonl` and `.cdr/index/task.jsonl`.
