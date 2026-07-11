---
last_synced_commit: 78a18180bf6e611b212a9ba4cba29af0904c1f5f
---

# LLD: `engine/catalog/`

Status: implemented (`engine/catalog/*.go`: `file.go`, `idalloc.go`, `catalog.go`,
`content.go`). See [HLD.md](../HLD.md) for system context.

## Purpose

On-disk metadata catalog for every file (topic document) HiveMind manages. This is the source of
truth for a file's identity, current version, size, and lifecycle status, and the anchor point
that `mvcc/`, `split/`, and `btree/` all coordinate through. Alongside the metadata catalog itself,
this package also owns `ContentStore`, the on-disk I/O layer for a file's actual markdown body
bytes (see [ContentStore](#contentstore-content-io) below).

## Storage layout

- Slotted 4KB pages (Postgres/SQLite-style layout), stored at `.meta/catalog.dat`
  (`DefaultCatalogFileName`), managed by `FileManager` (`file.go`).
- Page 0 is a dedicated free-list page: a bitmap where bit `i` tracks whether page
  `(i+1)` is currently allocated (1) or free (0). `AllocatePage` prefers reusing a
  freed page ID over extending the file; `FreePage` returns a page to the free-list
  for future reuse. The bitmap (plus its 8-byte `highestAllocated` header) is kept
  in memory and persisted durably (`WriteAt`+`Sync`) after every mutation, so it
  survives process restarts.
- `IDAllocator` (`idalloc.go`) hands out monotonically increasing `fileID`s and
  persists its high-water-mark in a small sidecar file (`<catalog.dat>.idalloc`,
  a single little-endian `uint64`), independent of `FileManager`'s own page-0
  bitmap layout (see [ID allocation](#id-allocation-idalloc) below).
- `ContentStore` (`content.go`) stores each file's actual markdown content as a
  separate file under a `content/` subdirectory, one file per `fileID`
  (`content/<fileID>.v1.md` — see [ContentStore](#contentstore-content-io)).

## Record shape

Each catalog record holds:

```
fileID          uint64   // monotonically increasing, atomic counter
pathHash
currentVersion
sizeBytes
status          ACTIVE | SPLITTING | SPLIT | REDIRECT
redirectTargetIDs []
parentTopicID
lastModified
```

`fileID` allocation is a monotonically increasing atomic counter — no reuse, no gaps-matter
semantics (see [ID allocation](#id-allocation-idalloc)).

## CRUD API (`catalog.go`)

`Catalog` is the striped-mutex CRUD API over `CatalogRecord`s, built on top of
`Page`, `FileManager`, and `IDAllocator`. `Put`/`Get`/`Delete` operate on a
single current record per `fileID` — no history/versioning of a `fileID`'s past
records (that is MVCC's concern, see [mvcc.md](mvcc.md)) and no WAL logging of
its own (WAL-before-apply for catalog mutations is layered on top by
`ContentStore` and other callers — see [WAL-before-apply](#wal-before-apply)).

- `Put(rec)` inserts or overwrites the record for `rec.FileID`. If a record
  already exists for that `fileID`, `Put` always deletes the old slot and
  inserts a fresh one (delete-then-reinsert), rather than attempting an
  in-place update — a deliberate simplicity-over-efficiency tradeoff. No
  history of the overwritten record is kept.
- `Get(fileID)` / `Delete(fileID)` return a wrapped `ErrNotFound` if no record
  exists for `fileID`.
- `CompareAndSwapCurrentVersion(fileID, expected, newVersion)` is the atomic
  CAS `mvcc/` performs on `currentVersion` when a write commits, serialized
  under the same per-`fileID` stripe `Get`/`Put` use.

### Locking model

Three logically distinct locks protect different resources — worth spelling out
explicitly because it is easy to conflate "the striped locks" with "the index
lock":

1. **`stripes [numStripes]sync.Mutex`** (`numStripes = 256`), keyed by
   `stripeFor(fileID) = fileID % numStripes`. This is the "striped mutexes
   (~256 stripes, hashed by `fileID`) instead of one global lock" design goal:
   it protects the per-`fileID` read-modify-write critical section (e.g. two
   concurrent `Put`s for the same `fileID`, or a `Put` racing a `Delete` for the
   same `fileID`) against torn reads/lost updates. Operations on `fileID`s that
   hash to different stripes never contend on this lock. Plain modulo (rather
   than a proper hash of `fileID`) is a deliberate simplicity choice:
   `fileID`s are allocated by a monotonically increasing counter
   (`IDAllocator.Next()`), so they are already well-distributed across stripes
   by allocation order in the common case.
2. **`pageStripes [numPageStripes]sync.Mutex`** (`numPageStripes = 256`), keyed
   by `pageStripeFor(pageID) = pageID % numPageStripes`. This is orthogonal to
   `stripes` above: many distinct `fileID`s' records can be packed onto the
   same physical page (`page.go`'s slotted layout), so two operations on
   *different* `fileID`s' stripes can still race on the same underlying
   physical page (e.g. one `fileID`'s tombstone racing another `fileID`'s
   insert into the same shared active page) unless that specific `pageID`'s
   read-modify-write sequence (`ReadPage` -> mutate in-memory `Page` ->
   `WritePage`) is itself serialized. Keying by `pageID` (rather than reusing
   the `fileID` stripes) keeps this narrowly scoped to pages that are
   *actually* shared, so operations on different `fileID`s whose records live
   on different pages still proceed without blocking each other.
3. **`indexMu sync.RWMutex`**, guarding the in-memory `index map[uint64]location`
   (`fileID` -> page/slot) itself. This is a completely different concern from
   (1): a Go map is never safe for concurrent access regardless of how many
   per-`fileID` stripes exist above it, so the lookup structure needs its own
   (brief, map-operation-only) protection. It is NOT one of the 256 stripes and
   must not be conflated with them — it is only ever held briefly, around the
   map read/write itself.

`FileManager`'s own internal lock (`file.go`'s `mu`, see
[FileManager and the striped-mutex scoping fix](#filemanager-and-the-striped-mutex-scoping-fix)
below) is a fourth, independent lock, but `Catalog` does not need to wrap any
locking of its own around `FileManager` calls — see that section for why.

### `activeMu` and the residual insert-path serialization caveat

A fourth field, `activeMu sync.Mutex`, guards `activePageID`: the single shared
"current page being appended into for new inserts" cursor used by `insert`
(the helper `Put` calls to physically place a new record). Every call to
`insert` — regardless of which `fileID`/stripe/page it is ultimately headed
for — takes this single, unstriped `activeMu` for its full duration (deciding
whether the current active page has room via `tryInsertInto`, and, on a miss,
allocating a fresh page via `FileManager.AllocatePage` and making it the new
active page).

This is a deliberate, but real, **residual serialization point**: unlike the
`stripes` and `pageStripes` locks above, `activeMu` is not keyed by `fileID` or
`pageID` at all, so concurrent `Put` calls for *any* two `fileID`s that both
need to physically insert a new slot serialize against each other here, even
if they hash to different stripes and would otherwise touch different pages.
In other words, the "unrelated files never contend on the same lock" design
goal fully holds for `Get`/`Delete`/CAS (which never touch `activeMu`) and for
`Put`'s tombstone-old-slot half, but not for `Put`'s insert-new-slot half,
which always funnels through this one shared cursor. A single shared active
page (rather than one per stripe) is a deliberate simplicity choice for this
subtask's scope; per-stripe (or per-page-stripe) active pages would reduce
this contention further, but that optimization is left to a later subtask,
once real throughput needs justify the added bookkeeping complexity of
tracking multiple concurrently-active pages. Once inside `insert`, the actual
page I/O for a freshly-allocated page is still additionally guarded by
`pageStripes` (defense-in-depth/consistency with every other
`ReadPage`/`WritePage` sequence in this file), but that does not change the
fact that `activeMu` itself is held, unstriped, across the whole call.

## `FileManager` and the striped-mutex scoping fix

`FileManager` (`file.go`) wraps the raw `*os.File` handle to `.meta/catalog.dat`
and has narrow internal locking: it is safe for concurrent use out of the box,
without requiring any external locking from callers. Only the genuinely shared,
file-wide bookkeeping state — `highestAllocated` and the free-list `bitmap` — is
guarded by `FileManager`'s own `mu`; the brief critical sections are:

- `AllocatePage`'s scan-for-a-free-bit-then-mark-used-and-persist sequence,
- `FreePage`'s clear-bit-and-persist sequence, and
- `validDataPageID`'s read of `highestAllocated`.

The actual page I/O (`ReadPage`/`WritePage`'s `pread`/`pwrite`/`fsync`) is **not**
serialized by `FileManager` at all: concurrent `ReadPage`/`WritePage` calls to
different, already-allocated pages proceed fully in parallel, because distinct
pages occupy non-overlapping byte ranges of the underlying file.

This is a fix, not the original design: an earlier version of this file had a
caller-side `fmMu sync.Mutex` in `Catalog` wrapping *every* `FileManager` call,
which incorrectly serialized all operations — including the expensive
synchronous `fsync` inside `WritePage` — across every page and `fileID`
regardless of which page or stripe they actually touched, directly
contradicting the "unrelated files never contend on the same lock" design
goal above. That caller-side lock has been removed; the fix narrows the lock
to live inside `FileManager` itself, scoped only to the bookkeeping state it
actually protects (`highestAllocated`, `bitmap`), not to page contents or
`fileID`-level concerns, which remain `Catalog`'s responsibility via its own
`stripes`/`pageStripes`. Concretely: `Catalog.readSlot`/`tombstone`/`insert`/
`tryInsertInto` call `FileManager` methods (`ReadPage`/`WritePage`/
`AllocatePage`/`FreePage`) directly, with no additional wrapping lock of their
own around them.

### `FreePage` double-free guard

`FreePage(pageID)` checks `isUsed(pageID)` before clearing the bitmap bit and
returns an explicit error if the page is already free, instead of silently
no-oping. This catches a caller (e.g. a future `split`/`mvcc` bug) that
erroneously calls `FreePage` twice on the same page: most dangerously, in the
window between the two calls another goroutine may have legitimately
`AllocatePage`'d and reused that same page ID for something else, and a
silent second free would let the erroneous caller re-free (and make eligible
for a third, conflicting reallocation) a page that is now actively in use.
`FreePage` also always rejects freeing the reserved free-list page (page 0)
and any `pageID` beyond `highestAllocated`.

## ID allocation (`idalloc.go`)

`IDAllocator` hands out monotonically increasing `fileID`s for use as
`CatalogRecord.FileID` values, never reusing an ID even after the catalog
record referencing it has been deleted. `Next()` serializes
"increment in memory + durably persist the new high-water-mark" (`WriteAt` +
`Sync` to the `<catalog.dat>.idalloc` sidecar) as a single critical section
under a plain mutex; if the durable persist fails, the in-memory counter does
not advance, so it can never get ahead of what is actually durable on disk.

A sidecar file was chosen over extending `FileManager`'s page-0 free-list
bitmap or borrowing a fixed regular data page: page 0's bitmap header is only
8 bytes with no meaningfully "free" padding to reuse, and borrowing a fixed
data page would create a fragile, implicit ordering dependency (only correct
if `IDAllocator` were always the very first caller of `AllocatePage`).

### Cross-check against `catalog.dat`'s actual max `FileID`

`NewIDAllocator` cross-checks the sidecar's restored high-water-mark against
`maxFileIDInCatalog(fm)` — the largest `CatalogRecord.FileID` found among all
non-tombstoned slots across every currently-allocated page of `catalog.dat` —
before returning. If `catalog.dat` contains a `fileID` higher than the
sidecar's high-water-mark, the sidecar cannot be trusted (it may have been
deleted/lost and recreated fresh, independently restored from a stale backup,
or simply never created against a `catalog.dat` that already had records in
it), and `NewIDAllocator` returns an explicit error rather than silently
risking a future `Next()` call handing out a `fileID` that collides with one
already recorded on disk. The reverse case (sidecar high-water-mark higher
than any `fileID` currently in `catalog.dat`) is normal, not an error — it just
means some previously allocated `fileID`s were never `Put`, or were `Put` and
later deleted. This scan runs once per `NewIDAllocator` call (i.e. once per
process startup/catalog reopen), not on every CRUD call.

## `ContentStore` (content I/O)

`ContentStore` (`content.go`) is the on-disk content (topic file body) I/O
layer that sits alongside `Catalog`: `Catalog` owns a `fileID`'s metadata
record, `ContentStore` owns the actual markdown bytes for that `fileID`, at
`ContentPath(fileID) = <root>/content/<fileID>.v1.md`. This subtask's scope is
deliberately pre-MVCC and single-version only: every method always
reads/writes the single `v1` file regardless of `CatalogRecord.CurrentVersion`;
multi-version content file naming keyed off `CurrentVersion` is left to the
MVCC-aware content-versioning work that builds on top of this layer.

### Create / Read / Append contract

- **`Create(rec, data)`** is the create/write path: it durably logs `rec` as a
  catalog `Put` mutation to the WAL (see
  [WAL-before-apply](#wal-before-apply)) and only then writes `data` to disk at
  `ContentPath(rec.FileID)` and makes `rec` visible via `cs.cat.Put`, in that
  order. It returns the WAL offset the record was appended at.

  **Duplicate-`fileID` semantics** (subtask 4.5.5.4): calling `Create` a
  second time for a `fileID` that already has a catalog record and/or content
  file is legal and intentionally performs a full, last-write-wins overwrite —
  it is **not** guarded by any already-exists check. This is safe rather than
  corrupting because both halves of the write are themselves safe overwrites:
  `writeContentFile` always writes to a fresh temp file and atomically renames
  it over `ContentPath(rec.FileID)`, and `cs.cat.Put(rec)` is `Catalog`'s own
  documented upsert (delete-old-slot-then-reinsert). The net effect of two
  `Create` calls for the same `fileID` is that the second call's data and
  `rec` entirely supersede the first's, byte-for-byte and field-for-field;
  nothing from the first call survives or leaks. Callers that need "create
  only if `fileID` does not already exist" semantics must check
  `cs.cat.Get(rec.FileID)` themselves before calling `Create`.

  `Create` does not need `ContentStore`'s striped locking (see
  [Concurrency](#concurrency-1) below): it is only ever called once per
  `fileID`, with a freshly-allocated `fileID` that by construction
  (`IDAllocator.Next()`'s monotonic allocation) cannot yet have a concurrent
  second `Create` call racing it for the same `fileID` — there is no existing
  content to race a read-modify-write against.

- **`Read(fileID)`** resolves `fileID` through the catalog first
  (`cs.cat.Get`), mirroring the catalog-is-source-of-truth convention `Create`
  relies on for visibility. A `fileID` with no catalog record is reported as a
  wrapped `ErrNotFound`. If the catalog record exists but the content file
  itself is missing or unreadable, that is reported as a distinct
  (non-`ErrNotFound`) error, indicating an internal inconsistency rather than
  "never created". `Read` never performs a read-modify-write, so it needs no
  striped locking: `writeContentFile`'s write-to-temp-then-rename technique
  makes a single `Read` always observe either the fully-old or fully-new
  content, never a torn/partial one.

- **`Append(fileID, data)`** is the append/mutate path (subtask 1.4.3): it
  reads `fileID`'s current content, appends `data`, durably logs the
  resulting record (with an updated `SizeBytes`) as a catalog `Put` mutation
  to the WAL, and only then writes the combined content to disk and makes the
  updated record visible via `cs.cat.Put` — the same WAL-before-apply
  discipline `Create` provides, on the same `wal.AppendAndApply` primitive.
  Like `Read`, a `fileID` with no catalog record is a wrapped `ErrNotFound`.
  `Append` returns `thresholdCrossed=true` exactly on the one call whose
  resulting size pushes the file from at-or-under `ContentStore`'s configured
  split threshold (`splitThresholdBytes`, defaulted to 8KB, matching
  [split.md](split.md)'s "Trigger" section) to strictly over it — a
  signal/stub for a future auto-split caller to act on, not actual splitting.
  `Append` also invalidates `fileID`'s cached header-offset index (see
  `ReadPartial` below) as part of the same WAL-covered apply step.

### `ContentStore`'s own striped locking (independent from `Catalog`'s)

`Append` (and `ReadPartial`, and `LockFileContent` — used by
`engine/split/execute.go`'s redirect-stub rewrite) perform a
read-existing/mutate/write-back sequence on `fileID`'s content file, which is
unsafe to run concurrently against itself for the *same* `fileID`: two
concurrent `Append`s could both read the same "existing" bytes and each write
back a result reflecting only their own appended data, silently losing the
other's update, with no error surfaced (`cat.Put` would happily accept
whichever write landed last).

`ContentStore` reuses this repo's striped-mutex convention to fix this, via
its own **independent** `stripes [numStripes]sync.Mutex` array (`content.go`),
keyed by the same `stripeFor(fileID)` `Catalog.stripes` uses — but a
*separate* array instance, not shared with `Catalog.stripes`. This is
required, not just tidy: `Append`'s critical section calls `cs.cat.Put`
internally, and `cs.cat.Put` takes `Catalog`'s *own* stripe lock for
`rec.FileID`; reusing the exact same lock instance would deadlock a
non-reentrant `sync.Mutex` on that call. `ReadPartial` takes the same
`cs.stripes[stripeFor(fileID)]` lock for its own critical section, so it can
never interleave with a concurrent `Append` (or a split's redirect-stub
rewrite, via `LockFileContent`) for the same `fileID`. Concurrent operations
on *different* `fileID`s still proceed in parallel in the common case
(different stripes), preserving the same "unrelated files never contend"
design goal `Catalog.stripes` already documents.

`headerCacheMu` is a separate, single (non-striped) mutex guarding the
in-memory `headerCache` map itself — analogous to `Catalog.indexMu`'s role
for `Catalog.index` — kept independent from `cs.stripes` specifically so
`InvalidateHeaderCache` can be called safely from within an already-held
`cs.stripes[stripe]` critical section (as `Append` does) without risking a
non-reentrant-mutex deadlock.

## WAL-before-apply

Every catalog mutation performed through `ContentStore` (`Create`, `Append`)
is logged in the WAL *before* it is applied — enforced structurally by
`wal.AppendAndApply` (not just by convention), matching
[wal.md](wal.md)'s invariant. Concretely: `wal.NewCatalogPutRecord(fileID,
encoded)` is durably appended and fsynced first; only inside
`AppendAndApply`'s apply callback does `ContentStore` write the content file
(`writeContentFile`) and make the record visible (`cs.cat.Put`). If the WAL
append itself fails, neither the content file nor the catalog record is
touched. If the WAL append succeeds but the apply callback fails, the WAL
record is already durable — recovery/replay of that record is `wal.Replay`'s
concern, not `ContentStore`'s. `Catalog` itself (`Put`/`Delete` called
directly, outside `ContentStore`) does **not** perform this WAL logging on
its own; the WAL-before-apply guarantee described here is specifically the
one `ContentStore` (and other WAL-aware callers) layer on top of `Catalog`'s
plain CRUD API.

## Concurrency

Four independent locking mechanisms are in play across this package, each
scoped to the state it actually protects rather than one broad lock — see
[Locking model](#locking-model),
[`FileManager` and the striped-mutex scoping fix](#filemanager-and-the-striped-mutex-scoping-fix),
and [`ContentStore`'s own striped locking](#contentstores-own-striped-locking-independent-from-catalogs)
above for the full breakdown:

1. `Catalog.stripes` (256, keyed by `fileID`) + `Catalog.indexMu` (map guard) +
   `Catalog.pageStripes` (256, keyed by `pageID`) for `Catalog`'s own CRUD API.
2. `Catalog.activeMu` — a single, unstriped lock serializing the
   insert-new-slot half of every `Put`, a real residual contention point (see
   [`activeMu` and the residual insert-path serialization caveat](#activemu-and-the-residual-insert-path-serialization-caveat)).
3. `FileManager.mu` — narrowly scoped to `highestAllocated`/`bitmap`
   bookkeeping only, never to page I/O itself.
4. `ContentStore.stripes` (256, keyed by `fileID`, independent from
   `Catalog.stripes`) + `ContentStore.headerCacheMu` (map guard) for
   `Append`/`ReadPartial`/`LockFileContent`.

`IDAllocator.mu` is a fifth, narrow, single-purpose lock (not one of the 256
catalog stripes) serializing its own increment-and-persist critical section;
see [ID allocation](#id-allocation-idalloc).

## Interactions with other modules

- `mvcc/` performs an atomic CAS on `currentVersion` when a write commits
  (`Catalog.CompareAndSwapCurrentVersion`).
- `split/` transitions a record's `status` to `SPLITTING`, then to `SPLIT`, and
  manages `redirectTargetIDs` for the redirect/stub left behind at the old
  path; `engine/split/execute.go`'s redirect-stub rewrite uses
  `ContentStore.LockFileContent` to take the same per-`fileID` stripe
  `Append`/`ReadPartial` use, closing a race window where a concurrent
  `ReadPartial` could otherwise observe a stale, about-to-be-invalidated
  header-offset cache entry.
- `btree/` maps topic path strings to `fileID`; the catalog is keyed by
  `fileID` and is the record-of-truth once a path resolves.
- `wal/`: every catalog mutation performed through `ContentStore` is logged in
  the WAL before being applied (see [WAL-before-apply](#wal-before-apply) and
  [wal.md](wal.md)).

## Known risks

- **Section-index staleness**: the markdown header-offset cache used by
  `ReadPartial` must be invalidated atomically within the same append/split
  transaction that changes a catalog record — otherwise `ReadPartial` can
  serve offsets against stale content. This is resolved by
  `ContentStore.InvalidateHeaderCache` being called from within the same
  WAL-covered apply closure at every content-changing call site (`Append`
  here, and `ExecuteSplitRedirectStub`/`ExecuteSplitAtomic` in
  `engine/split/execute.go`, both additionally serialized via
  `ContentStore.LockFileContent`/`stripes`). See [split.md](split.md).
- **`activeMu` residual serialization**: as documented above, every `Put`
  that needs to physically insert a new slot funnels through the single,
  unstriped `activeMu` lock, regardless of `fileID`/stripe. This does not
  violate correctness, but it is a real (currently accepted) contention point
  that the "unrelated files never contend on the same lock" design goal does
  not fully achieve for `Put`'s insert path. A per-stripe (or
  per-page-stripe) active-page scheme would close this gap but is deferred
  until real throughput needs justify the added bookkeeping.
- **No page-enumeration-based index rebuild on load**: `NewCatalog` does not
  scan `.meta/catalog.dat` on load to rebuild its in-memory `index` from
  records that already exist on disk; a fresh `Catalog` starts with an empty
  in-memory index. Adding a page-enumeration API to `FileManager` (or a
  persisted directory/index page in `catalog.dat` itself) is left to a later
  subtask, plausibly alongside `wal/`'s recovery story.

## Cross-references

- [HLD.md](../HLD.md) — system-level architecture
- [mvcc.md](mvcc.md) — versioning built on top of catalog records
- [split.md](split.md) — status transitions during auto-split
- [wal.md](wal.md) — durability guarantee for catalog mutations
- [btree.md](btree.md) — path -> fileID lookup that resolves into the catalog
