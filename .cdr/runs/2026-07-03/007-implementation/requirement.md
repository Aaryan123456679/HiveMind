# Requirement — Subtask 1.1.2

Source: GitHub issue #1 ("Catalog slotted-page implementation (`engine/catalog/`)"),
Epic "Phase 1: Storage core (single-threaded)".

## Subtask 1.1.2 — Slotted 4KB page implementation

Header, slot array, insert/read/delete slot, free-space tracking.

**Acceptance criteria:**
- Page supports `InsertSlot`/`ReadSlot`/`DeleteSlot` within a 4KB buffer.
- Tracks free space correctly.
- Rejects inserts that would overflow the page (no panic, no silent truncation —
  same "no data loss" precedent set by subtask 1.1.1's `Encode`/`Decode` error
  handling).

**Test spec:**
`go test ./engine/catalog/... -run TestSlottedPage -race`
- Insert until full, then assert the next insert returns an overflow error.
- Delete a slot + reinsert: confirm the freed slot space is actually reused.

**Impacted modules:** `engine/catalog/page.go`, `engine/catalog/page_test.go`.

**Out of scope for this subtask** (deferred to 1.1.3+):
- File manager / `.meta/catalog.dat` open-create/page-allocation/free-list page.
- FileID allocator.
- Striped-mutex CRUD API.

## Context from prior subtask (1.1.1, verified)

`engine/catalog/record.go` defines `CatalogRecord` and `RecordEncodedSize` (a small
fixed-size encoded byte blob computed from byte offsets in the code; exact value not
duplicated here to avoid drift). Slotted pages will later store such encoded records,
but this subtask only implements the generic slot mechanism — it must not import or
depend on `CatalogRecord` directly (page.go stores opaque `[]byte` slot payloads).
