# Requirement — Subtask 1.1.1

Source: GitHub issue #1 — "[1] Catalog slotted-page implementation (engine/catalog/)"
Epic/Milestone: Phase 1: Storage core (single-threaded)

## Subtask 1.1.1 — Catalog record struct + fixed-size binary encode/decode

**Acceptance criteria:**
`CatalogRecord{fileID, pathHash, currentVersion, sizeBytes, status, redirectTargetIDs,
parentTopicID, lastModified}` encodes to and decodes from a fixed-size byte layout with no
data loss for all fields, including empty/zero `redirectTargetIDs`.

**Test spec:**
```
go test ./engine/catalog/... -run TestRecordEncodeDecode -race
```
covering round-trip encode->decode equality for populated and zero-value records.

**Impacted modules:** `engine/catalog/record.go`, `engine/catalog/record_test.go`

**Explicitly out of scope for this subtask** (future subtasks in the same issue, each its own
commit): 1.1.2 slotted 4KB page, 1.1.3 catalog file manager (.meta/catalog.dat), 1.1.4 monotonic
fileID allocator persistence, 1.1.5 striped-mutex CRUD API.
