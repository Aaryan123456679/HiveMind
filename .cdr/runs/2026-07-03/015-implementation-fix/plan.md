# Plan: narrow FileManager locking (fix for subtask 1.1.5 CHANGES_REQUESTED finding)

## Problem recap
`catalog.go`'s `fmMu sync.Mutex` wrapped every call into `*FileManager`
(`ReadPage`/`WritePage`/`AllocatePage`/`FreePage`), including `WritePage`'s
synchronous `fsync` — the dominant per-operation cost. This serialized ALL
operations across ALL fileIDs/pages behind one lock, contradicting
docs/LLD/catalog.md's "unrelated files never contend" goal and the subtask's
literal acceptance criterion.

## Approach taken
1. **Moved synchronization into `FileManager` itself** (`engine/catalog/file.go`):
   added an unexported `mu sync.Mutex` field guarding ONLY the genuinely shared
   bookkeeping state: `highestAllocated` and the `bitmap` array. `mu` is held only
   for the brief bitmap-check/bitmap-mutation critical sections inside
   `AllocatePage` and `FreePage`, and for the `highestAllocated` read inside
   `validDataPageID` (used by both `ReadPage` and `WritePage`). It is explicitly
   NOT held around the `pread`/`pwrite`/`fsync` syscalls themselves — those run
   unsynchronized, which is safe because distinct pageIDs occupy non-overlapping
   file regions.
   - `persistBitmap` was renamed `persistBitmapLocked` and documented as requiring
     the caller to already hold `mu` (all call sites — `AllocatePage`, `FreePage`,
     and `Open`'s single-threaded init path — satisfy this).

2. **Removed `fmMu` entirely from `catalog.go`**: `readSlot`, `tombstone`,
   `insert`, and `tryInsertInto` now call `FileManager` methods directly with no
   Catalog-side lock around the FileManager call itself.

3. **Fixed the misleading doc comment** at the top of `catalog.go` describing the
   locking model: it now describes three real locks — per-fileID stripes
   (`stripes`, record-level serialization), the index-map `RWMutex` (`indexMu`),
   and FileManager's own internal narrow lock (documented as living in
   `file.go`, not accessible to or required of `Catalog`).

4. **Real correctness bug found and fixed while validating the narrow fix**: with
   `fmMu` removed, `TestCatalogConcurrentDistinctFileIDs` started failing
   (`slot 26 does not exist (slot count 26)`) — a genuine, previously-masked race.
   Multiple distinct fileIDs' records commonly share the same physical page (the
   whole point of a slotted page), and `readSlot`/`tombstone`/`insert`/
   `tryInsertInto` each perform a non-atomic `ReadPage -> mutate in-memory Page ->
   WritePage` sequence. Two different fileIDs' operations (e.g. one fileID's
   `tombstone` racing another fileID's `insert` into the shared active page) can
   target the SAME pageID from DIFFERENT stripe locks, since `stripeFor` is keyed
   by fileID, not by page. The old global `fmMu` accidentally also serialized this
   (by serializing literally everything), masking the bug.

   Fix: added a second, narrower striped lock, `pageStripes [256]sync.Mutex`
   keyed by `pageID % 256` (`pageStripeFor`), used by `readSlot`, `tombstone`,
   `tryInsertInto`, and the newly-allocated-page path in `insert` to make each
   pageID's read-modify-write sequence atomic with respect to any other operation
   touching that SAME pageID. This is orthogonal to both `stripes` (fileID-keyed)
   and `FileManager`'s own `mu` (bookkeeping-only): operations on fileIDs whose
   records live on DIFFERENT pages still never contend, satisfying the
   acceptance criterion; only operations that physically collide on the same
   page are serialized, which is a real, unavoidable requirement, not a
   regression of the fix's intent.

5. **Added a contention-proof test**
   (`TestCatalogFileManagerNarrowLockDoesNotSerializeAcrossIO`, in
   `engine/catalog/file_test.go`), mirroring
   `TestCatalogStripesDoNotSerializeAcrossDifferentFileIDs`'s technique. It uses a
   new test-only hook field, `FileManager.writeDelay func()` (nil in production,
   never set outside tests), invoked by `WritePage` right before its I/O (after
   `validDataPageID`'s brief `mu` section has already released). The test blocks
   one page's `WritePage` mid-flight via this hook and asserts that a concurrent
   `AllocatePage` call and a concurrent `ReadPage` call on a DIFFERENT,
   already-allocated page both complete quickly (bounded by a 2s timeout) despite
   the in-flight "slow" I/O — proving `mu` is not held for the duration of page
   I/O.

## Scope discipline
Everything else from the original 1.1.5 implementation is unchanged: Put/Get/Delete
logic, not-found error handling via `ErrNotFound`, the per-fileID `stripes` lock,
`indexMu`, `activeMu`/`activePageID`, and Put's delete-then-reinsert tradeoff. The
`pageStripes` addition was necessary to preserve correctness once the over-broad
`fmMu` was removed; it does not reintroduce cross-fileID contention for the common
case of fileIDs whose records live on different pages.
