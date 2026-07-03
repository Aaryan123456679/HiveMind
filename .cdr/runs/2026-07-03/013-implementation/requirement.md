# Requirement — Subtask 1.1.5

Source: GitHub issue #1, "Catalog slotted-page implementation (engine/catalog/)",
Epic "Phase 1: Storage core (single-threaded)".

## Subtask 1.1.5 — Striped-mutex catalog CRUD API (Put/Get/Delete a CatalogRecord by fileID)

> Per docs/LLD/catalog.md: "Striped mutexes (~256 stripes, hashed by `fileID`) instead
> of one global lock, so unrelated files never contend on the same lock."

### Acceptance criteria
- A `Catalog` type exposes:
  - `Put(rec CatalogRecord) error`
  - `Get(fileID uint64) (CatalogRecord, error)`
  - `Delete(fileID uint64) error`
- Concurrent operations on DIFFERENT fileIDs proceed without blocking each other
  (demonstrable via a test using ~256 stripes and asserting no single global lock
  serializes unrelated fileIDs).
- Concurrent operations on the SAME fileID are correctly serialized (no lost updates,
  no torn reads).
- Records are durably persisted via the existing `Page`/`FileManager` primitives
  (records live inside slotted pages, located via a simple in-memory
  fileID -> (pageID, slotID) index built during catalog load, or however deemed best,
  documented).
- `Get` on a nonexistent fileID returns a clear not-found error.
- `Delete` on a nonexistent fileID returns a clear not-found error (not a panic, not a
  silent no-op success).

### Test spec
`go test ./engine/catalog/... -run TestCatalog -race`:
1. Put+Get+Delete round-trip for a single record.
2. Concurrent Put/Get/Delete across many distinct fileIDs from many goroutines with no
   data races and no corruption.
3. A stripe-contention test demonstrating that operations on different fileIDs mapping
   to different stripes don't serialize behind each other.
4. Get/Delete on a nonexistent fileID returns the expected not-found error.

### Impacted modules
`engine/catalog/catalog.go`, `engine/catalog/catalog_test.go`.

### Explicit non-goals (per launch instructions)
- No MVCC versioning (that's `mvcc/`, a later subtask).
- No split logic (that's `split/`).
- No WAL logging (that's `wal/`).
- Purely: wire together the already-verified CatalogRecord (1.1.1), Page (1.1.2),
  FileManager (1.1.3), and IDAllocator (1.1.4) into a working single-record CRUD layer
  with striped-lock concurrency. `Put` overwrites the current record for a fileID, full
  stop — no versioning of a record's history in this subtask.

### Prior verified state (from .cdr/index/task.jsonl)
- task-1.1.1 (record.go/record_test.go) — verified, commit f974b495a53f41262f4b4963766c19c94cbfba76
- task-1.1.2 (page.go/page_test.go) — verified (PASS_WITH_COMMENTS), commit cc0d14ee09c2f44d7ce280d27838dc365678a329
- task-1.1.3 (file.go/file_test.go) — verified (PASS_WITH_COMMENTS), commit f7e9ba1a0ecb35cc0d46728adecbeeaae1e34c1b
- task-1.1.4 (idalloc.go/idalloc_test.go) — verified (PASS_WITH_COMMENTS), commit 0503b189d49a22891d024bf6915064f161c3fd8d
  - Flagged regression risk: NewIDAllocator has no cross-check against catalog.dat's
    actual max FileID if the sidecar (.idalloc) goes missing. Not fixed here; noted in
    catalog.go doc-comment per launch instructions.
