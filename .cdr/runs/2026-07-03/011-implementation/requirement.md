# Requirement — Subtask 1.1.4

Source: `gh issue view 1` (Epic "Phase 1: Storage core (single-threaded)", milestone "Phase 1:
Storage core (single-threaded)"), subtask **1.1.4 — Monotonic fileID allocator persisted across
restarts**.

## Acceptance criteria (verbatim from issue)

> fileID is an atomically-incrementing counter with no reuse/gaps semantics that matter; after
> process restart, the next allocated fileID continues strictly after the highest previously
> allocated ID.

Expanded per task brief:
- Allocator hands out strictly increasing fileIDs starting from 1 (0 reserved as a
  sentinel/invalid value).
- Never reuses an ID even after records are deleted (no compaction/recycling of fileIDs, unlike
  page IDs in the free-list).
- Safe for concurrent callers: atomic, no lost updates, no duplicate IDs under concurrent
  goroutines.
- Persists its high-water-mark durably so a re-open of the catalog continues from the correct
  next value rather than restarting at 1 (which would risk fileID collisions with
  already-persisted catalog records).

## Test spec (verbatim from issue, refined in task brief)

`go test ./engine/catalog/... -run TestFileIDAllocator -race`:
1. Sequential allocation returns strictly increasing values starting at 1.
2. Concurrent allocation from many goroutines (100 goroutines x 100 allocations = 10,000 calls)
   yields exactly that many unique IDs, no duplicates.
3. Persisting the allocator state and reopening (new `FileManager` + `IDAllocator` on the same
   underlying file) restores the correct next-ID high-water-mark — no collision with previously
   allocated IDs.

## Impacted modules

`engine/catalog/idalloc.go`, `engine/catalog/idalloc_test.go`.

## Explicit out-of-scope (per task brief, deferred to later subtasks)

- Striped-mutex catalog CRUD API (1.1.5).
- MVCC versioning (Phase 2A).
