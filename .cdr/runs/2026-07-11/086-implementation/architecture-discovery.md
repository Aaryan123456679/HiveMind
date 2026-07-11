# Architecture discovery (brief — docs-only subtask)

Read in full before editing:

- `docs/LLD/catalog.md` (current: scaffold-level, front-matter
  `last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317`).
- `engine/catalog/content.go` (580 lines): `ContentStore` struct doc comment
  (independent striped `stripes` array vs. `Catalog.stripes`, `headerCacheMu`),
  `Create`/`createWithHook` (WAL-before-apply via `wal.AppendAndApply`,
  4.5.5.4's duplicate-fileID last-write-wins doc comment), `Read`, `Append`
  (read-modify-write under `cs.stripes[stripeFor(fileID)]`, threshold-crossing
  signal, header-cache invalidation), `LockFileContent` (exported hook for
  `engine/split/execute.go`), `ReadPartial`, `writeContentFile` (temp+rename
  atomic write).
- `engine/catalog/file.go` (317 lines): `FileManager` doc comment describing
  the striped-mutex/narrow-locking fix (`mu` guards only `highestAllocated`/
  `bitmap`, never page I/O) replacing a prior caller-side `fmMu` that
  over-serialized; `AllocatePage`/`FreePage` (with 4.5.5.1's `isUsed`
  double-free guard)/`ReadPage`/`WritePage`/`validDataPageID`.
- `engine/catalog/idalloc.go` (218 lines): `IDAllocator`, sidecar file
  rationale, `NewIDAllocator`'s 4.5.5.2 cross-check against
  `maxFileIDInCatalog`, `Next()`.
- `engine/catalog/catalog.go` (466 lines, partially compressed in tool
  output — retrieved via headroom for lines 1-200; read lines 380-465
  directly): `Catalog` struct's three-lock model (`stripes`, `pageStripes`,
  `indexMu`) doc comment, `activeMu`/`activePageID` doc comment, `Put`,
  `insert` (activeMu-guarded, unstriped — the residual serialization point),
  `tryInsertInto`.

Style/format reference: `docs/LLD/wal.md` (recently LLD-synced, non-scaffold,
267 lines) used as the primary style model for a fully-synced, implemented-status
LLD doc (numbered/lettered subsections, inline code identifiers, explicit
cross-references, "Known risks" section retained). `docs/LLD/btree.md` checked
too but is still scaffold-level itself, so less useful as a target-state model.

No source code changes; no impact analysis beyond the doc itself required.
