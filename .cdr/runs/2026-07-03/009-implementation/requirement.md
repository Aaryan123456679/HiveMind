# Requirement — Subtask 1.1.3

Source: `gh issue view 1` — "[1] Catalog slotted-page implementation (engine/catalog/)",
Epic: Phase 1: Storage core (single-threaded).

## Subtask 1.1.3 — Catalog file manager: `.meta/catalog.dat` open/create + page allocation + free-list page

- **Acceptance criteria**:
  - Opening a non-existent `catalog.dat` creates an initial free-list page.
  - Allocating a new page marks it used in the free-list.
  - Deleting/merging a slot returns the page to free-list reclamation (at the
    file-manager level: `FreePage(pageID)` returns a page to the free-list for reuse).
- **Test spec**: `go test ./engine/catalog/... -run TestCatalogFileManager -race`:
  create a fresh catalog file, allocate N pages, delete some records, verify the
  free-list reclaims/reuses page slots on next allocation.
- **Impacted modules**: `engine/catalog/file.go`, `engine/catalog/file_test.go`.

## Explicit out-of-scope (deferred to later subtasks)

- 1.1.4 — fileID allocator (atomic, monotonically increasing, no reuse) —
  `engine/catalog/idalloc.go`.
- 1.1.5 — Catalog CRUD API + striped-mutex layer — `engine/catalog/catalog.go`.
- WAL/durability logging beyond a real `WriteAt`/`Sync` of the free-list page itself
  (full WAL is a separate later phase, `engine/wal/`).
- Concurrency/locking of the `FileManager` itself — this phase is single-threaded;
  `-race` is run only to catch accidental data races in test setup, not to validate
  concurrent-safety guarantees (that lands with striped locking in 1.1.5+).

## Prior subtasks (context, already verified)

- 1.1.1 (`record.go`): `CatalogRecord` + fixed-size binary encode/decode. Verified,
  commit `f974b495a53f41262f4b4963766c19c94cbfba76`.
- 1.1.2 (`page.go`): slotted 4KB `Page` type with `InsertSlot`/`ReadSlot`/`DeleteSlot`/
  `FreeSpace`/`SlotCount`. Verified (PASS_WITH_COMMENTS), commit
  `cc0d14ee09c2f44d7ce280d27838dc365678a329`.
