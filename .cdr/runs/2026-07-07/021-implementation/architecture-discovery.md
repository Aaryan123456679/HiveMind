# Architecture Discovery — Subtask 2b.3.4

## 1. Module shape: new package under the existing `engine` module

`engine/go.mod` declares a single module `github.com/Aaryan123456679/HiveMind/engine`
(Go 1.26.4). There is no `go.work` file in the repo (checked at repo root and
`engine/`). `engine/graph/` already exists as a directory containing only
`doc.go` (`// Package graph is part of the HiveMind storage engine.`) — a
stub placeholder, not yet a real package. Conclusion: this subtask adds
`edge_append.go` / `edge_append_test.go` as new files inside the existing
`graph` package; no new Go module or workspace entry is needed.

## 2. WAL-reuse vs. standalone append-only log

Reviewed `engine/wal/`: it has two layers.

- **Low-level (`writer.go`)**: `Writer`/`OpenWriter` — a generic,
  content-agnostic append-only, size-rotated segment file writer
  (`wal-<N>.log`), with a fixed 8-byte header (length + CRC32) per record,
  fsync-before-return on every `Append`, and `ReadSegment` to parse a
  segment's raw record payloads back out. This layer knows nothing about
  catalog/btree semantics — it just durably appends and reads back opaque
  `[]byte` payloads.
- **High-level (`record.go` + `recovery.go`)**: `TypedRecord`/`RecordType`
  (`RecordCatalogPut`, `RecordCatalogDelete`, `RecordBTreeInsert`,
  `RecordBTreeDelete`) and `Replay`, which dispatches decoded records by
  type to catalog/btree-specific `apply` callbacks. This is the shared
  crash-recovery machinery that subtask 1.3.x built specifically for the
  catalog+btree mutation set, and it currently hard-validates
  (`isValidRecordType`) against exactly those four types.

**Decision: reuse the low-level `wal.Writer`/`wal.OpenWriter`/`wal.ReadSegment`
primitives directly for durability, but do NOT touch or extend
`RecordType`/`TypedRecord`/`Replay`.**

Rationale:
- Reusing `Writer` avoids reinventing this repo's already-established
  WriteAt+fsync+segment-rotation durability idiom (exactly the kind of
  redundant lower-level work "minimal" should avoid).
- Extending the shared `RecordType` enum (adding e.g. `RecordGraphEdge`) and
  `isValidRecordType`/`Replay` would couple the shared, already-shipped
  catalog/btree crash-recovery path to a still-unwired, standalone
  `engine/graph` primitive that nothing calls yet (2b.3.5 hasn't wired
  `engine/split/execute.go` to it). That is scope creep beyond "minimal
  append-only primitive" and risks destabilizing tested recovery code for a
  consumer that doesn't exist yet.
- 2b.3.6's acceptance criterion ("commit entire split under one WAL-covered
  transaction") is a *future* integration concern for when `execute.go`
  wires allocation + catalog + btree + graph writes together — at that
  point it can decide whether graph edges share the split transaction's WAL
  directory/segment or use a distinct one. Nothing here forecloses that
  option: `edge_append.go` takes a `dir string` (own directory), exactly
  like `wal.OpenWriter`, `catalog.Open`, etc. already do independently.
- Using `wal.ReadSegment` (generic, payload-agnostic) for the minimal
  read-back the test needs is safe and requires zero changes to `wal/`.

This keeps `engine/graph` additive-only: it imports `engine/wal` for its
segment-file durability primitive, but the shared WAL recovery/typed-record
machinery is completely untouched (`gofmt`/`go vet`/`go test ./wal/...`
unaffected).

## 3. Edge structural shape

Per the issue's compressed subtask text (`{targetFileID, SPLIT_SIBLING|REDIRECT}`)
and the design-guidance in this run's task brief, and matching this
codebase's existing enum-naming convention (`engine/catalog/record.go`'s
`RecordStatus` — `uint8` iota-based, `StatusXxx` names, "package: message"
wrapped errors):

```go
type EdgeType uint8

const (
    EdgeTypeInvalid EdgeType = iota // zero value reserved, same convention as wal.RecordTypeInvalid
    EdgeSplitSibling
    EdgeRedirect
)

type Edge struct {
    Source uint64
    Target uint64
    Type   EdgeType
}
```

`Source`/`Target` are catalog fileIDs (`uint64`, matching `catalog.CatalogRecord.FileID`
and `RedirectTargetIDs []uint64`). A fixed-width 17-byte encoding (8+8+1)
mirrors this repo's existing little-endian fixed-header binary conventions
(`catalog/record.go`, `btree/node.go`).

The zero value `EdgeTypeInvalid` is reserved as a non-valid sentinel — matching
`wal.RecordTypeInvalid`'s stated rationale (an unset/garbage type should fail
closed on decode, not silently decode as a meaningful edge type).

## 4. Why this stays minimal (not Epic 3 scope creep)

Explicitly NOT built here (deferred to Epic 3 per the issue):
- CSR (compressed sparse row) storage layout.
- Segment/log compaction.
- Any traversal or multi-hop query API (find-neighbors, BFS, etc.).
- A general "read all edges for fileID X" query API — the test's read-back
  need is satisfied by a single minimal `ReadAll(dir) ([]Edge, error)`
  helper that re-reads and decodes an entire append-only directory
  (functionally the same shape as `wal.ReadSegment`+decode, just typed),
  used only to prove durability/ordering in
  `TestMinimalEdgeAppend`. This is not a query API — no filtering, no
  indexing, no per-fileID lookup.

Built here:
- `Edge` / `EdgeType` types.
- `AppendEdge(dir string, edge Edge) error` (or an `EdgeAppender` wrapping a
  `wal.Writer`) — a durable, append-only, ordering-preserving write
  primitive.
- Minimal `ReadAll` for the test's own durability/ordering assertions.
