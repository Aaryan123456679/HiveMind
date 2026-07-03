# Architecture Discovery — Subtask 1.1.1

Read order followed: `.cdr/memory/*` -> `docs/HLD.md` -> `docs/LLD/catalog.md` ->
`.cdr/index/file.jsonl` / `.cdr/index/task.jsonl` -> current `engine/catalog/` state.

## `.cdr/memory/*`
All memory files (state.md, pending.md, decisions.md, timeline.md, regression-routes.md,
impact-map.md) are empty stubs — fresh project, no prior implementation decisions to
reconcile.

## `docs/HLD.md`
- `catalog/` = "On-disk metadata catalog, slotted 4KB pages, striped-mutex concurrency"
  (see LLD/catalog.md).
- `engine/` Go module houses catalog, btree, graph, mvcc, split, wal, rpc, loadtest as
  sibling packages.
- Explicitly a custom on-disk storage engine (not delegating to an embedded DB).

## `docs/LLD/catalog.md`
- Status noted as "scaffold only" (doc.go placeholder) — matches current repo state.
- Storage layout: slotted 4KB pages (Postgres/SQLite-style), stored at `.meta/catalog.dat`,
  with a free-list page for reclaiming deleted/merged slots. (Out of scope here — subtask
  1.1.2/1.1.3.)
- Record shape (as specified in LLD): fileID (uint64, monotonic atomic counter), pathHash,
  currentVersion, sizeBytes, status (ACTIVE | SPLITTING | SPLIT | REDIRECT),
  redirectTargetIDs [], parentTopicID, lastModified.
- Concurrency: striped mutexes (~256 stripes hashed by fileID) — out of scope for 1.1.1
  (lands in 1.1.5).
- Interactions: mvcc/ CASes currentVersion; split/ transitions status
  ACTIVE->SPLITTING->SPLIT and populates redirectTargetIDs; btree/ maps path->fileID;
  wal/ requires mutations logged before applied. None of these dependents exist yet, so
  this subtask has no existing callers to keep compatible with — pure additive.

## Indexes
- `.cdr/index/file.jsonl`: no existing entries for engine/catalog/*.go (only doc entries
  for docs/LLD/*.md from the documentation run). Confirms record.go/record_test.go are new.
- `.cdr/index/task.jsonl`: has one line per issue (`task-1.1` for issue #1, still
  `"state": "planned"` from the 002-planner run), but no per-subtask (1.1.1 etc.) rows yet.
  This run adds the first sub-task-granularity row (`task-1.1.1`) and marks it
  `implemented`.

## Current state of `engine/catalog/`
Only `doc.go` exists (package declaration + one-line doc comment). No record type, no
tests, no page/file/idalloc/catalog CRUD code — matches LLD's "scaffold only" note.
`engine/go.mod` declares module `github.com/Aaryan123456679/HiveMind/engine`, go 1.26.4.

## Design decisions for this subtask (judgment calls, documented per task instructions)
- `pathHash`: `uint64` (not `[32]byte`) — simplest fixed-size choice sufficient for a first
  pass; a full content hash can be layered later without changing this subtask's contract.
- `status`: typed `RecordStatus uint8` with named constants
  `StatusActive=0, StatusSplitting=1, StatusSplit=2, StatusRedirect=3`.
- `redirectTargetIDs`: fixed-capacity array `[MaxRedirectTargets]uint64` (constant = 8) plus
  a `uint8` count field, to keep the record fixed-size while covering the LLD's split
  fan-out expectation (typically 2-3 children, "room to spare").
- `lastModified`: `int64` Unix nanoseconds (`time.Time.UnixNano()`), fixed-size, matches
  "standard for on-disk formats" idiom.
- Byte order: little-endian throughout via `encoding/binary`.
- API shape: `Encode() []byte` and `Decode(data []byte) (CatalogRecord, error)` — idiomatic,
  matches instructions; `Decode` validates buffer length and count bounds before use.
