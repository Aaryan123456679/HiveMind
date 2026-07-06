# Architecture Discovery — Subtask 2b.3.1

## Input types consumed (read-only dependency, `engine/split/proposer.go`, committed `02b6e2d`)

- `SplitPlan{ Files []SplitFileProposal; RedirectSummary string }`
- `SplitFileProposal{ NewPath string; SectionRanges []SectionRange }`
- `SectionRange{ Start, End int }` — **half-open** `[Start, End)` byte-offset range into
  the *original* file's content, ordinary Go slice-indexing convention (confirmed by
  proposer.go's doc comment on `SectionRange`).
- `proposer.go` explicitly states fileID/redirect-target allocation and content/stub
  file writes are issue #12's job — i.e. exactly this subtask's job for the
  allocation+write half.

## Test fixtures available (read-only dependency, `engine/split/proposer_mock.go`, committed `3c83d72`/`f8abd00`)

- `FixtureFileContent = []byte("fixture file content!!!!")` (24 bytes).
- `FixtureSplitPlan`: two `SplitFileProposal`s, `fixture-part-1.md` -> `[0,12)`,
  `fixture-part-2.md` -> `[12,24)` — disjoint, contiguous, exactly tiling
  `FixtureFileContent`. Directly reusable as `TestSplitAllocateAndWrite`'s fixture.

## fileID allocation convention (`engine/catalog/idalloc.go`)

- `catalog.IDAllocator` hands out monotonically increasing `uint64` fileIDs via
  `Next()`, starting at 1 (0 == `catalog.InvalidFileID`, reserved sentinel). Never
  reused, even after deletion.
- `Next()` durably persists the new high-water-mark (`WriteAt` + `Sync` to a small
  sidecar `.idalloc` file) *before* returning success, so a crash between allocating
  and using an ID never risks a later collision on reopen.
- Constructed via `catalog.NewIDAllocator(fm *catalog.FileManager)`. It is a shared,
  composition-root-level dependency (see `engine/integration_test.go`'s usage
  alongside `Catalog`/`ContentStore`/`wal.Writer`) — every other package that needs
  new fileIDs (e.g. `engine/btree/insert.go`) takes an `*catalog.IDAllocator` as an
  explicit constructor/function dependency rather than inventing its own scheme.
  **Decision: `engine/split/execute.go` follows this exact convention** — it accepts
  a caller-supplied `*catalog.IDAllocator` rather than defining a second allocator.

## Durable content-file write convention (`engine/catalog/content.go`)

- Content lives at `<root>/content/<fileID>.v1.md` (`ContentStore.ContentPath`,
  exported).
- `ContentStore.writeContentFile` (unexported) is the actual durable-write
  primitive: `os.CreateTemp` a sibling `<fileID>.v1.*.md.tmp` file in the same
  directory, `Write`, `Sync`, then `os.Rename` into the final path. Rename is atomic
  on the same filesystem, so a crash mid-write can never leave a torn/partial file
  visible at the final path — this is the repo's general durable-write idiom (also
  referenced for `engine/catalog/file.go`'s `WriteAt`+`Sync` convention for pages).
- `ContentStore.Create`/`Append`, however, are NOT reusable as-is for this subtask:
  both durably log a `CatalogRecord` mutation to the WAL and then call `cs.cat.Put`
  as part of the *same* critical section (WAL-before-apply invariant). Using them
  here would prematurely create catalog records for the new split-off files, which
  is explicitly 2b.3.2's job, not 2b.3.1's. `writeContentFile` itself is also
  unexported, so it cannot be called directly from `engine/split`.
- **Decision:** 2b.3.1 defines its own small, local temp-file+rename write helper in
  `engine/split/execute.go`, mirroring `writeContentFile`'s exact pattern
  (CreateTemp -> Write -> Sync -> Rename) but writing ONLY the content file, with no
  catalog interaction at all. It computes the destination path via
  `catalog.ContentStore.ContentPath(newFileID)` (exported) so the new file lands at
  the exact same path 2b.3.2 will later expect when it wires up the catalog record
  pointing at that fileID — but this subtask never calls `cs.cat.Put`, `cs.Create`,
  or `cs.Append`.

## WAL (`engine/wal/`)

- `wal.AppendAndApply` is the repo's WAL-before-apply primitive, used by
  `ContentStore.Create`/`Append` and `split/orchestrate.go`'s `transitionStatus` for
  every *catalog* mutation.
- Per the issue's own subtask breakdown, "commit entire split as a single
  WAL-covered...transaction" is explicitly 2b.3.6's acceptance criterion, not
  2b.3.1's. Wrapping this subtask's file writes in WAL now would be premature:
  there is no catalog record yet to make the write meaningful/visible to readers,
  and 2b.3.6 will need to cover allocation + content writes + catalog + B+Tree +
  graph as one atomic unit anyway — a partial WAL wrapping now would just be
  discarded/rebuilt then.
- **Decision:** 2b.3.1 does not touch `engine/wal/` at all. Crash-safety in
  isolation for its own primitive (a torn/partial content file) is still achieved
  via the temp-file+rename technique above, matching the repo's existing
  crash-safety posture for individual file writes (distinct from cross-step
  transactional atomicity, which is out of scope here).

## Scope boundary recap

This subtask is a narrow, pure primitive:
`ExecuteSplitAllocateAndWrite(idAlloc, cs, originalContent, plan) (map[string]uint64, error)`
— for each `SplitFileProposal` in `plan.Files`: validate its `SectionRanges` against
`originalContent`'s bounds and against every other proposal's ranges (disjointness),
allocate one new fileID via `idAlloc.Next()`, concatenate the byte slices for that
proposal's ranges (in order), and durably write that content to
`cs.ContentPath(newFileID)` via temp-file+rename. Returns a map from `NewPath` to
newly allocated fileID so a later subtask (2b.3.2) can wire up catalog records
without re-deriving IDs. Does not touch `catalog.Catalog`, `engine/btree`,
`engine/graph`, or `engine/wal`.
