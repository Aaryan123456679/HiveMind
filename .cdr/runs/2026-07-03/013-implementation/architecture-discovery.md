# Architecture Discovery — Subtask 1.1.5

## Read order followed
1. `.cdr/memory/*` — all empty (no prior decisions/pending/regressions recorded yet
   for this repo's memory layer; nothing to reconcile).
2. `docs/HLD.md`, `docs/LLD/catalog.md` — confirms striped-mutex design (~256 stripes,
   hashed by fileID), slotted 4KB pages at `.meta/catalog.dat`, free-list page,
   fileID as monotonic atomic counter, no reuse.
3. `.cdr/index/file.jsonl`, `.cdr/index/task.jsonl` — confirms task-1.1.1..1.1.4 are
   `verified` with commits; task-1.1.5 not yet present (added by this run).
4. `engine/catalog/record.go` — `CatalogRecord` struct, `Encode() ([]byte, error)`,
   `Decode([]byte) (CatalogRecord, error)`, `RecordEncodedSize` constant (currently
   8*4 + 1 + 1 + 2 + 8*8 + 8 + 8 = 122 bytes: FileID/PathHash/CurrentVersion/SizeBytes
   (8 each) + Status(1) + RedirectCount(1) + reserved(2) + RedirectTargetIDs(8*8=64)
   + ParentTopicID(8) + LastModified(8)).
5. `engine/catalog/page.go` — `Page` (4096-byte), `InsertSlot(data []byte) (int, error)`,
   `ReadSlot(slotID int) ([]byte, error)` (returns a COPY, confirmed by 1.1.2
   verification note in the doc comment: "Page has no internal locking... ReadSlot
   returns a copy"), `DeleteSlot(slotID int) error` (tombstone-only, does not compact),
   `FreeSpace() int`. Slot IDs are stable and never reused within a page (tombstoned
   slots' directory entries persist).
6. `engine/catalog/file.go` — `FileManager`, `AllocatePage() (uint64, error)`,
   `FreePage(pageID uint64) error`, `ReadPage(pageID uint64) (*Page, error)`,
   `WritePage(pageID uint64, p *Page) error`. Page 0 reserved for free-list bitmap;
   data pages start at 1. `FileManager` has no internal locking (explicitly documented
   as the responsibility of "a future subtask (striped-mutex CRUD)" — i.e. this one).
7. `engine/catalog/idalloc.go` — `IDAllocator`, `NewIDAllocator(fm *FileManager)
   (*IDAllocator, error)`, `Next() (uint64, error)` (durably persists high-water-mark
   via sidecar `.idalloc` file). 1.1.4's verification flagged: no cross-check against
   catalog.dat's actual max FileID if the sidecar goes missing. Not addressed here per
   launch instructions; a doc-comment cross-reference is left in catalog.go instead
   (see below), since Catalog is now the component that actually writes FileID values
   into pages and would be a reasonable place to eventually add such a cross-check.

## Key design decisions for this subtask

### 1. In-memory location index
`type location struct { pageID uint64; slotID int }`
`Catalog` holds `index map[uint64]location` guarded by a single `sync.RWMutex`
(`indexMu`). This is a genuinely distinct lock from the 256 striped record-mutexes:
- `indexMu` protects the *lookup structure itself* (the Go map) from concurrent
  read/write corruption — a map is never safe for concurrent access without external
  synchronization, regardless of how many stripes exist above it.
- The 256 `stripes [256]sync.Mutex` protect the *per-fileID read-modify-write critical
  section* against torn reads/lost updates when multiple goroutines operate on the
  SAME fileID concurrently (e.g. two concurrent Puts to fileID 42, or a Put racing a
  Delete on fileID 42).

These two locks answer different questions ("is the map consistent?" vs "is this
fileID's data consistent?") and conflating them would either (a) force all index
lookups to serialize behind an unrelated fileID's stripe (defeating the whole point of
striping) or (b) allow torn per-fileID updates if only the map were protected.

### 2. Stripe selection
`stripe := fileID % numStripes` (`numStripes = 256`). Documented as deliberately simple
modulo rather than a hash function: fileIDs are allocated by `IDAllocator.Next()` as a
monotonically increasing counter (already well-distributed by allocation order across
stripes), so plain modulo distributes evenly in the common case. A doc-comment notes
that a proper hash of fileID (e.g. fnv/xxhash) would be more robust against adversarial
or pathological ID patterns if that property is ever needed — not needed now, not built
now.

### 3. Put semantics: delete-then-reinsert (chosen over in-place update)
`Put` always removes any existing slot for the fileID (if present) and inserts a fresh
slot via `Page.InsertSlot`, rather than trying to update in-place when the new encoded
size happens to fit the old slot's page. This is documented in `catalog.go` as a
deliberate simplicity-over-efficiency tradeoff: `Page.InsertSlot`'s tombstone-reuse path
already gives partial space efficiency (a slot's old space is reclaimed by a
same-page or later insert), and a smarter "update in place iff same page has room" is
explicitly deferred to a later subtask once the compaction/page-allocation story
matures (mirrors the deferral language already used in page.go's own doc comments for
full compaction).

Since `CatalogRecord.Encode()` always produces a FIXED-size buffer
(`RecordEncodedSize` is a compile-time constant, not variable-length per record), the
delete-then-reinsert path is simple: old slot (if any) is tombstoned via
`Page.DeleteSlot`, then the encoded record is inserted via the shared "active page"
(see below), and the index is updated to the new (pageID, slotID) atomically under the
combination of the stripe lock (already held) + the index lock.

### 4. Page allocation for new inserts: single shared "active page" cursor
`Catalog` maintains `activePageID uint64` and a cached `*Page` in memory
(`activePage *Page`), guarded by a dedicated `activeMu sync.Mutex` (distinct from both
the stripes and the index lock, since which page is "active for new inserts" is
orthogonal to any specific fileID or to the index-lookup-map itself). `Put` computes
the encoded bytes, then under `activeMu`: if `activePage.FreeSpace()` has room for
`slotHeaderSize`-plus-`RecordEncodedSize` (approximated conservatively by just
attempting `InsertSlot` and falling back on error), insert there; otherwise call
`FileManager.AllocatePage()` for a new page, write the old page back first, and make
the new page active. This is documented as intentionally NOT per-stripe active pages
(that would be a natural read/write-amplification optimization for a later subtask,
but a single shared active page is correct and sufficient for 1.1.5's acceptance
criteria, which do not require throughput at that granularity).

### 5. Locking protocol summary (documented in code)
- `Put(rec)`: lock `stripes[stripe(rec.FileID)]` -> (encode) -> lock `activeMu` for the
  page write/insert -> unlock `activeMu` -> lock `indexMu` (write) to update the index
  and to look up/remove any prior location -> unlock `indexMu` -> unlock stripe.
- `Get(fileID)`: lock `stripes[stripe(fileID)]` (recommended per launch guidance, for
  linearizability with a concurrent Put's delete-then-reinsert sequence) -> lock
  `indexMu` (read) to find location -> unlock `indexMu` -> `ReadPage`+`ReadSlot`
  (no lock needed for the read itself since `ReadSlot` returns a copy and the page
  object read from disk for this call is a fresh `*Page` from `FileManager.ReadPage`,
  not shared mutable state, EXCEPT when the location refers to the current active page,
  in which case we must read via the in-memory `activePage` under `activeMu` rather
  than re-reading a stale on-disk copy that hasn't been flushed yet) -> unlock stripe.
- `Delete(fileID)`: lock `stripes[stripe(fileID)]` -> lock `indexMu` (write) to look up
  and remove -> if not found, unlock everything and return `ErrNotFound` -> otherwise
  tombstone the slot (reading/writing the correct page: active page in memory, or via
  `FileManager.ReadPage`+`WritePage` for a non-active page) -> unlock.

### 6. Not-found error
`var ErrNotFound = errors.New("catalog: fileID not found")`, wrapped with the specific
fileID via `%w` for context, so callers can `errors.Is(err, catalog.ErrNotFound)`.

## Stripe-contention test technique chosen
Rather than relying on wall-clock timing races (flaky under `-race`/CI load), the test
adds a package-level (test-only) hook: `Catalog` exposes an unexported test hook via a
build-tag-free approach — a simple exported-for-test `StripeTestDelay` is avoided to
keep the production type clean; instead the test achieves deterministic proof of
non-serialization by:
1. Directly acquiring `catalog.stripeLockForTest(fileIDA)` (a small unexported test
   helper in `catalog_test.go` within the same package, since Go tests in-package can
   reach unexported fields) and holding it in one goroutine.
2. Concurrently issuing `Put`/`Get` for `fileIDB` that hashes to a DIFFERENT stripe from
   a second goroutine, asserting via a channel + `select` with a short timeout that the
   second operation completes well before the first goroutine releases its held lock —
   i.e. it does NOT block on the first goroutine's held stripe.
3. As a cross-check, the reverse is also asserted: operations on the SAME fileID as the
   held stripe DO block until release (proving stripes are real locks, not no-ops).

This is more convincing than a raw timing/benchmark-only approach because it uses an
explicit "lock is held until I say so" signal (a channel close) rather than a fuzzy
duration threshold, eliminating flakiness while still exercising the exact same
`stripes [256]sync.Mutex` field the production Put/Get/Delete methods use.
