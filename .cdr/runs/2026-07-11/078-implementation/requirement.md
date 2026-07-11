# Requirement — Subtask 4.5.5.4

GitHub issue #42 ("Phase 4.5: Storage-engine technical debt", milestone #10).

## 4.5.5.4 — Add ContentStore.Create duplicate-fileID semantics test/guard

- Acceptance criteria: Calling `Create` twice for the same fileID has explicitly
  documented and tested semantics — either an intentional legal overwrite
  (documented in the doc comment) or an already-exists guard returning an
  error. Also adds empty-content and very-large-content input coverage.
- Test spec: `go test ./engine/catalog/... -run TestContentCreateDuplicateFileID`
  and `TestContentCreateEmptyAndLargeContent`.
- Impacted modules: `engine/catalog/content.go`, `engine/catalog/content_test.go`

## Origin

This subtask directly resolves a previously-recorded low-risk regression finding
from run 2026-07-04/047-verification (subtask 1.4.1):

> "ContentStore.Create has no duplicate/already-exists guard: calling Create
> twice for the same fileID silently overwrites both content/<fileID>.v1.md
> (via writeContentFile's temp+rename) and the catalog record (Catalog.Put is
> an upsert). No test exercises this or empty/very-large content inputs."
> Recommendation: "Add a test asserting the intended semantics of a duplicate
> Create on an existing fileID (either document it as legal overwrite, or add
> an already-exists guard), and add empty/large-content coverage."

## Investigation mandate

Must read the ACTUAL current source of `ContentStore.Create` (not assume) and
decide path (a) document-as-overwrite or (b) add-guard, based on evidence.

## Scope isolation

Only touch `engine/catalog/content.go` and `engine/catalog/content_test.go`.
Do not touch file.go, idalloc.go, catalog.go, or engine/split, engine/btree,
engine/wal, engine/mvcc, engine/rpc. Stage explicitly (no `git add -A`/`.`).
Test scope: `go test ./engine/catalog/... -race` only.
