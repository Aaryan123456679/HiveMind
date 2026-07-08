# Plan — Subtask 3.1.1: CSR-like compact adjacency array in `graph.dat`

## Design decision

`graph.dat` is a single whole-snapshot binary file, atomically rewritten (temp+fsync+rename,
following `catalog/content.go:writeContentFile`'s precedent) on every `WriteCSR` call — not an
append-only log. Rationale: CSR arrays are, by nature, a compacted structure that periodic
compaction (3.1.3) rebuilds wholesale from the accumulated edge log; there is no meaningful
"append one edge to a CSR array" operation without breaking the offset-array's contiguity
invariant, so this subtask does not attempt incremental updates.

### On-disk layout

```
Header (28 bytes, all integers little-endian):
  [0:4]   magic       "GCS1" (ASCII) - format identifier + implicit major version marker
  [4:8]   version      uint32 = 1 (format version, independent of magic, for future bumps)
  [8:16]  nodeCount    uint64 - number of distinct source fileIDs with adjacency entries
  [16:24] edgeCount    uint64 - total number of edges across all nodes
  [24:28] payloadCRC   uint32 - CRC32(IEEE) of every byte that follows the header

Payload (immediately follows header):
  nodeIDs   : nodeCount   * 8  bytes - sorted ascending source fileIDs (uint64 LE each)
  offsets   : (nodeCount+1) * 8 bytes - CSR offsets array (uint64 LE each);
              offsets[i]..offsets[i+1] is the half-open range, in the edges array,
              of nodeIDs[i]'s neighbors; offsets[0] == 0, offsets[nodeCount] == edgeCount
  edges     : edgeCount * 21 bytes - flat neighbor array, one fixed-width record each:
                Target      uint64 (8 bytes, LE)  - target fileID
                Type        byte   (1 byte)       - graph.EdgeType (reuses edge_append.go's enum)
                Weight      uint32 (4 bytes, LE)  - edge weight (for ENTITY_COOCCUR increments,
                                                     per docs/LLD/graph.md's edge shape; 3.1.1
                                                     just persists whatever weight it's given,
                                                     doesn't compute increments itself)
                LastUpdated int64  (8 bytes, LE)  - unix seconds, per LLD's edge shape
```

This directly matches classic CSR ("compressed sparse row"): an offsets array indexed by node
position + a flat neighbors array, giving O(1) lookup of a node's neighbor slice and compact
contiguous storage with no per-edge pointer overhead.

### Why these specific choices

- **Magic + version header, CRC32 over payload**: mirrors `engine/wal/writer.go`'s established
  header+CRC32(IEEE) convention (this repo's one durability idiom for detecting bit-level
  corruption), applied here to a whole-file snapshot instead of a per-record log entry. A single
  CRC over the whole payload (not per-section) is sufficient and simpler, since the whole file is
  atomically replaced on every write (no partial-record recovery scenario like WAL's segment log
  has — either the whole rename lands or it doesn't).
- **Atomic temp+rename (not WAL segment log)**: `graph.dat` is reloaded wholesale on restart
  (per acceptance criteria: "reloads correctly after a process restart"), so whole-file atomicity
  via rename is both simpler and sufficient — no torn-write recovery logic is needed, unlike
  `engine/wal`'s incrementally-appended segments. This follows `catalog/content.go`'s
  `writeContentFile` precedent exactly (temp file in same dir, `Write`+`Sync`+`Close`, then
  `os.Rename`, with `os.Remove(tmpPath)` cleanup on any error).
- **Sorted nodeIDs + parallel offsets array (classic CSR), not a map serialization**: keeps the
  on-disk format compact and directly reloadable into either a map-based or binary-search-based
  in-memory index without re-deriving offsets; sorting also gives deterministic byte-identical
  output for identical input adjacency (useful for the round-trip test and future diffing/
  debugging).
- **Edge record carries Weight + LastUpdated now, not just Target+Type**: `docs/LLD/graph.md`'s
  edge shape (`{targetFileID, edgeType, weight, lastUpdated}`) already specifies these fields for
  the graph's edges in general (weight increments come from 3.1.3's ENTITY_COOCCUR compaction
  logic, not from 3.1.1). Persisting the full shape now avoids a breaking on-disk format
  migration when 3.1.3 lands; 3.1.1 itself is agnostic to how weight/lastUpdated are computed —
  it just persists and reloads whatever `CSREdge` values it's given.
- **Reuses `graph.EdgeType`** (from `edge_append.go`, same package) rather than defining a new
  enum, since issue #15 states the enum will grow (3.1.4 adds `EntityCooccur`/`LLMAsserted`) —
  no need for a second type family.
- **Does not touch or import `EdgeAppender`/WAL**: per scope boundary, this subtask is the CSR
  array format only; the append-only edge log (3.1.2) and compaction-from-log-into-CSR (3.1.3)
  are separate, later subtasks. `csr.go` has zero dependency on `edge_append.go` or `engine/wal`
  beyond copying its CRC32/little-endian conventions inline (no shared import needed for that).

### Public API surface (`engine/graph/csr.go`)

- `type CSREdge struct { Target uint64; Type EdgeType; Weight uint32; LastUpdated int64 }`
- `type CSRGraph struct { ... }` (unexported fields: sorted nodeIDs, offsets, flat edges)
- `func BuildCSR(adjacency map[uint64][]CSREdge) *CSRGraph` — constructs from an in-memory
  adjacency map (per-source-fileID edge lists), sorting node IDs deterministically.
- `func (g *CSRGraph) Neighbors(fileID uint64) []CSREdge` — O(log n) lookup + O(1) slice of the
  node's edges (binary search over sorted nodeIDs).
- `func (g *CSRGraph) NodeCount() int`, `func (g *CSRGraph) EdgeCount() int` — small accessors
  used by tests/future callers.
- `func WriteCSR(path string, g *CSRGraph) error` — atomic temp+fsync+rename write.
- `func LoadCSR(path string) (*CSRGraph, error)` — reads header, validates magic/version,
  validates payload CRC32, decodes into a `*CSRGraph`.

## Test plan (`csr_test.go`)

1. `TestCSRFormat` (required by issue): build adjacency for several fileIDs (including a node
   with zero out-edges present in the ID set, multiple edge types, weight/lastUpdated values),
   `WriteCSR`, `LoadCSR` from a fresh `*CSRGraph` reference (simulating "process restart" per
   acceptance criteria), assert `Neighbors` output is identical per node to the original input.
2. `TestCSREmptyGraph` — build+write+load a graph with zero nodes/edges; assert it reloads without
   error and `NodeCount()==0`, `EdgeCount()==0`.
3. `TestCSRCorruptedPayloadDetected` — write a valid file, flip a byte in the payload region,
   assert `LoadCSR` returns an error (CRC mismatch caught), not silently-wrong data.
4. `TestCSRLargeAdjacency` — larger synthetic graph (many nodes, many edges per node) to exercise
   the offsets-array math beyond trivial sizes, assert round-trip correctness.
5. `TestCSRTruncatedHeaderRejected` — a file shorter than the fixed header size is rejected with
   an error, not a panic/garbage read.

## Non-goals for 3.1.1 (explicitly deferred)

- Incremental/append updates to `graph.dat` (3.1.2/3.1.3's job).
- Any traversal/query API beyond direct `Neighbors(fileID)` lookup (3.1.5's `GraphNeighbors` with
  depth/filter/cap is separate).
- Concurrency/locking around shared `graph.dat` access.
