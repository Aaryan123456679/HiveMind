# Architecture Discovery — Subtask 1.1.4

## Read order followed

1. `.cdr/memory/state.md`, `decisions.md`, `pending.md` — all effectively empty placeholders;
   no prior open decisions/blockers relevant to idalloc.
2. `docs/HLD.md` — system-level context: `engine/catalog` is the on-disk metadata catalog,
   striped-mutex CRUD (later subtask), coordinates with `mvcc/`, `split/`, `btree/`.
3. `docs/LLD/catalog.md` — confirms: "fileID allocation is a monotonically increasing atomic
   counter — no reuse, no gaps-matter semantics"; record shape has `fileID uint64` as first
   field; free-list page for page reclamation is a *page*-ID concept, orthogonal to fileID.
4. `.cdr/index/file.jsonl`, `.cdr/index/task.jsonl` — confirmed 1.1.1 (record.go), 1.1.2
   (page.go), 1.1.3 (file.go) all `verified`; no existing entry for 1.1.4.
5. Current source:
   - `engine/catalog/record.go` — `CatalogRecord.FileID uint64` is the field this allocator's
     `Next()` output is intended to populate on record creation (CRUD wiring happens in the
     later 1.1.5 subtask, not here).
   - `engine/catalog/page.go` — 4096-byte slotted page, `PageSize` constant, no fileID-related
     state.
   - `engine/catalog/file.go` — `FileManager` wraps `.meta/catalog.dat`. Page 0 is reserved for
     the free-list bitmap (`freeListPageID = 0`); bitmap header is only 8 bytes
     (`bitmapHeaderSize = 8`, storing `highestAllocated`), and the **entire remainder of page 0**
     (4088 of 4096 bytes) is consumed by `bitmapCapacityBits = (PageSize - bitmapHeaderSize) * 8`
     — i.e. every bit of the rest of page 0 already backs a real page-ID's used/free flag. There
     is *no* meaningfully "free padding" in page 0 to reuse without shrinking the free-list's
     addressable page-ID space (a functional regression, not just a cosmetic one) or invalidating
     1.1.3's verified on-disk layout/tests.
   - Data pages start at page 1 and are handed out by `FileManager.AllocatePage()` /
     reclaimed by `FreePage()`; `ReadPage`/`WritePage` require `pageID` to be a currently
     allocated, non-zero page.

## Design decision: sidecar state file, not a bitmap-page extension or borrowed data page

Rejected options and why:

- **(a) Extend page 0's bitmap header** to add an 8-byte next-fileID field: would shrink
  `bitmapCapacityBits` (fewer addressable page IDs) and — more importantly — would change
  `file.go`'s on-disk layout and durability code path that 1.1.3 already implemented and had
  independently verified (`task-1.1.3` = `verified`, `PASS_WITH_COMMENTS`). Touching that file
  risks violating the instruction not to disturb the bitmap's own bytes/durability semantics
  from 1.1.3, and this subtask's impacted-modules list is scoped to `idalloc.go`/
  `idalloc_test.go` only.
- **(b) Reserve a fixed data page (e.g. page 1) via `AllocatePage`/`WritePage`**: page 1 is only
  guaranteed to be the *first* page `AllocatePage` will ever hand out if `IDAllocator` is always
  the very first caller against a brand-new `FileManager`, every single time, for the life of the
  system — an implicit, fragile ordering dependency on every future caller of `FileManager`
  (including the 1.1.5 CRUD layer and beyond) never allocating a page before `IDAllocator` does.
  There is no way to *re-discover* which page ID holds the allocator's state on a later reopen
  without already knowing it up front, so this would require a second piece of hardcoded,
  cross-subtask contractual state anyway.

Chosen: a small **sidecar file**, `<catalog-path>.idalloc`, opened/created by `IDAllocator`
itself via the exact same `os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)` +
`WriteAt`+`Sync` durability pattern `FileManager` already uses for its own bitmap page (i.e.
reusing the *pattern*, even though `FileManager`'s own `ReadPage`/`WritePage` methods can't be
reused verbatim since they operate on `PageSize`-aligned data pages and this state is a single
8-byte value that has nothing to do with the slotted-page/free-list page space). This:
- Never touches `file.go`/`page.go` (zero risk to 1.1.3's verified bitmap semantics/tests).
- Never competes with real data-page allocation (no page-ID budget consumed, no fragile
  first-caller ordering assumption).
- Is trivially discoverable on reopen: derived deterministically from the catalog file's own
  path (`fm.file.Name() + ".idalloc"`), which `idalloc.go` can read since it lives in the same
  `catalog` package (`FileManager.file` is unexported but package-visible).
- Uses the identical low-level durability primitive (`WriteAt` + `Sync`) `FileManager` itself
  relies on, satisfying "prefer reusing existing I/O primitives" in spirit even though the
  concrete `ReadPage`/`WritePage` API doesn't fit an 8-byte non-page-aligned value.

## Concurrency approach

A single `sync.Mutex` guards "read `next`, increment, persist, commit new value" as one critical
section. Given `Next()` must synchronously fsync a durable write on every call, a mutex-protected
critical section is strictly simpler and equally correct compared to a lock-free
`atomic.Uint64` (which would need a separate mechanism to keep the durable value from lagging the
in-memory value). This mirrors the task brief's explicit guidance that a mutex here is *not* the
"no global lock" striped-mutex CRUD path (1.1.5) — it is a narrow, single-purpose allocator lock.
