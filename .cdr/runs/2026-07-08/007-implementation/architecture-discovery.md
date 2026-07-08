# Architecture discovery — 3.1.3 compaction

## Sources read (index-first order)
- `docs/HLD.md` (system context, graph module role) — read via grep for "graph".
- `docs/LLD/graph.md` — storage layout, CSR format section (3.1.1), edge log
  section (3.1.2), edge shape / edge types, traversal API, known risks.
- `.cdr/runs/2026-07-08/004-implementation/` .. `006-cdr-commit/` — 3.1.2's
  artifact trail (edge log design, `EdgeLog` API surface, decisions already
  made about `wal.Writer` reuse and per-node isolation).
- Direct source reads (required — this subtask sits at the csr.go/edgelog.go
  interface): `engine/graph/csr.go`, `engine/graph/edgelog.go`,
  `engine/graph/edge_append.go` (EdgeType enum lives here), `engine/wal/writer.go`,
  `engine/wal/checkpoint.go` (precedent for "durably record how much has been
  consumed, then archive only what's covered").

## Existing building blocks this subtask composes

- `csr.go`: `CSRGraph`, `BuildCSR(map[uint64][]CSREdge) *CSRGraph`,
  `WriteCSR(path, *CSRGraph) error` (already atomic: temp file + fsync +
  rename), `LoadCSR(path) (*CSRGraph, error)`. `CSREdge{Target, Type, Weight,
  LastUpdated}`.
- `edgelog.go`: `EdgeLog{root, mu, writers map[uint64]*wal.Writer}`,
  `OpenEdgeLog(root)`, `AppendEdge(sourceFileID, CSREdge)`,
  `ReadNode(sourceFileID) ([]CSREdge, error)`, `nodeDir(sourceFileID)`,
  `listWALSegments(dir)`. No enumerate-all-nodes API exists yet — compaction
  needs one (added as an unexported helper in `compact.go`, reusing the
  `<root>/<fileID>/` directory-naming convention `nodeDir` already
  establishes, since fileIDs are the subdirectory names).
- `edge_append.go`: `EdgeType` enum (`EdgeTypeInvalid`, `EdgeSplitSibling`,
  `EdgeRedirect`) — extended by this subtask with `EdgeEntityCooccur` and
  `EdgeLLMAsserted` (see requirement.md for why).
- `wal.Writer`/`wal.OpenWriter`: no delete/reset primitive exists. Truncating
  a per-node log after compaction means: close the cached `*wal.Writer` (if
  open), delete its on-disk segment files, drop it from `EdgeLog.writers`, so
  the next `AppendEdge` for that fileID lazily reopens a fresh, empty
  `wal.Writer` at segment 0 (mirrors `OpenWriter`'s own "resuming vs.
  brand-new dir" logic in `latestSegmentNum`).

## Decision 1 — weight-aggregation semantics

The issue text says: "weight increments on repeated ENTITY_COOCCUR edges."
Read together with `CSREdge.Weight` (a caller-supplied `uint32`, not a
count derived internally by this package — `csr.go`'s doc comment: "this
package only persists and reloads whatever values it is given") and
`docs/LLD/graph.md`'s framing of `ENTITY_COOCCUR` as "incremented when
ingestion... extracts co-occurring entities across files", the natural
reading is: each `AppendEdge` call for an `ENTITY_COOCCUR` edge represents
one fresh co-occurrence observation (typically appended with `Weight: 1`),
and compaction's job is to fold every occurrence of the same
`(sourceFileID, Target, Type=ENTITY_COOCCUR)` triple — both what's already
in the existing `graph.dat` (from a prior compaction) and everything
currently sitting in that node's edge log — into ONE CSR edge entry whose
`Weight` is the SUM of every merged occurrence's `Weight`, and whose
`LastUpdated` is the MAX (most recent) `LastUpdated` across the merged set.
This is "increment" in the literal sense the issue uses it, and it is the
only reading under which "repeated ENTITY_COOCCUR edges" produce a
meaningfully growing weight rather than a constant/overwritten one.

For all OTHER edge types (`LLM_ASSERTED`, `SPLIT_SIBLING`, `REDIRECT`): a
repeated `(source, target, type)` triple is deduplicated to a single CSR
entry (never emitted twice — satisfies "without losing or duplicating
edges") but its `Weight`/`LastUpdated` are NOT summed — the most recent
occurrence (by `LastUpdated`, ties broken by log-order recency) wins,
last-write-wins. Rationale: these edge types are structural/assertional,
not occurrence-counted; the issue's weight-increment language is scoped
explicitly to `ENTITY_COOCCUR`, and summing weights for e.g. `SPLIT_SIBLING`
edges (which docs/LLD/graph.md describes as created once per split event,
not incrementally) would be semantically wrong and untested by the issue's
own test spec.

## Decision 2 — crash-safety ordering

Compaction reads (a) the existing `graph.dat` via `LoadCSR` (or starts from
an empty adjacency map if no `graph.dat` exists yet) and (b) every per-node
edge log's pending entries via `EdgeLog.ReadNode`, merges them per Decision
1, builds a fresh `CSRGraph` via `BuildCSR`, and writes it via the EXISTING
atomic `WriteCSR` (temp + fsync + rename — already crash-safe from 3.1.1,
unchanged here). The crux ordering decision: per-node edge logs are ONLY
truncated (reset to empty) AFTER `WriteCSR` has durably renamed the new
`graph.dat` into place — never before, and never interleaved with the
write. Consequences:

- Crash before `WriteCSR`'s rename completes: the old `graph.dat` (or no
  `graph.dat`) is untouched, and every edge log is untouched (still holds
  every not-yet-compacted entry). Retrying compaction from scratch is safe:
  no data loss, and — because compaction reads to build a full snapshot
  rather than incrementally appending — no duplication either.
- Crash after rename, before edge-log truncation: `graph.dat` now durably
  contains all the merged edges, but the edge logs still also hold them
  (not yet truncated). This is safe-by-idempotence-of-retry too: on next
  compaction run, the same entries get re-read from the (still full) logs,
  re-merged with the same weight-summing rule... but WAIT — this would
  double-count `ENTITY_COOCCUR` weights that already made it into the new
  `graph.dat`, since Decision 1 always starts a fresh merge from
  `LoadCSR`'s already-compacted weight plus every log entry's weight again.
  This is why truncation must happen (and the design records it as a
  known, accepted risk if truncation itself is interrupted): a crash in
  the narrow window between rename-success and truncation-success can
  cause a subsequent, retried compaction to double-count NOT-YET-TRUNCATED
  entries a second time. This module accepts this narrow-window risk
  (matching the WAL package's own general precedent — `wal.Checkpoint`'s
  entire purpose is to mark "durably applied up to here" as a SEPARATE,
  best-effort step after the underlying mutation is already durable, with
  the same class of narrow-window risk documented in `checkpoint.go`) rather
  than trying to make truncation part of the same atomic operation as
  `WriteCSR`'s rename (which is infeasible: truncation touches N separate
  per-node log directories, not a single file). Per-node truncation is
  therefore performed independently, one node at a time, immediately after
  the rename succeeds; each per-node truncation failure is collected but
  does not roll back the already-durable `graph.dat` (the new snapshot is
  correct regardless — only the redundant not-yet-truncated log entries
  remain, to be re-merged, at worst harmlessly re-summed, on the next run).
  A code comment on `Compact` documents this explicitly as the accepted
  crash-safety boundary, matching the issue's ordering guidance: "a crash
  mid-compaction just leaves the OLD graph.dat intact and the edge logs not
  yet truncated (safe to retry, no data loss)" for the crash-before-rename
  case (the common, expected crash window), with the narrow
  post-rename-pre-truncation double-count risk called out as a documented,
  accepted limitation (not silently ignored).

## Enumerating all nodes with pending log entries

`EdgeLog` has no "list all fileIDs with logs" method (by design — `OpenEdgeLog`
is deliberately O(1), not eager-enumerating). `compact.go` adds its own
directory-listing helper, `edgeLogNodeIDs(root string) ([]uint64, error)`,
that lists `root`'s immediate subdirectories and parses each name as a
`uint64` fileID (mirroring `nodeDir`'s own naming convention:
`filepath.Join(root, strconv.FormatUint(sourceFileID, 10))`). This does not
require any change to `EdgeLog`'s public API for read access, but `compact.go`
DOES need a truncate primitive that `EdgeLog` doesn't expose today, so a new
exported method, `EdgeLog.TruncateNode(sourceFileID uint64) error`, is added
to `edgelog.go` (closes+drops the cached writer if any, then removes the
per-node directory's on-disk segment files).
