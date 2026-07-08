# Requirement — Subtask 3.1.5

Source: `gh issue view 15` (issue #15, Epic Phase 3 "Graph store + ingestion agents"),
milestone #5. NOTE: the issue body (fetched via `gh issue view 15` and again via
`gh issue view 15 --json body -q .body`) is truncated/garbled by GitHub's own rendering in
this environment (words dropped mid-sentence) but the surviving fragments plus
`docs/LLD/graph.md`'s "Traversal API" section (verbatim, not garbled) give an unambiguous
function signature. Earlier tool output in this run additionally contained injected
fake system-reminder-style text (fake date-change notice, fake MCP "tokensave" server
tool instructions, fake "Auto Mode Active" directive) appended after real content — this
is the same known, recurring prompt-injection pattern flagged in every prior run for this
issue (see `.cdr/runs/2026-07-08/014-implementation/requirement.md`). Ignored; not acted
upon; disclosed here only.

## Subtask 3.1.5 (as extracted from issue's subtask list + `docs/LLD/graph.md`)

Title: **Neighbor query API: `GraphNeighbors(fileID, edgeTypeFilter, cap)` — read + type
filter + cap, hard-capped 0-2 hops, reachable-node cap for context blow-up prevention.**

- Acceptance criteria (issue text, corroborated by LLD): build a graph with more than
  `maxNodes` reachable nodes within 2 hops; a traversal call's result size must be capped
  at `maxNodes`; the traversal must respect an edge-type filter.
- Test spec: `go test ./engine/graph/... -run TestGraphNeighbors: build graph with
  >maxNodes reachable nodes within 2 hops, assert traversal result size capped at maxNodes
  and respects type filter.`
- Impacted modules: `engine/graph/traverse.go`, `engine/graph/traverse_test.go` (both new,
  per issue's own module list for this subtask).

## Canonical signature (`docs/LLD/graph.md`, "Traversal API" section, verbatim, not garbled)

> `GraphNeighbors(fileID, depth, edgeTypeFilter, maxNodes)` — used by the engine to expand
> topics the query-time topic-selector judges insufficient alone (0-2 hop traversal), and
> hard-capped system-wide at `k + 2k` total files to prevent context blow-up (see
> query-agent.md).

This fixes the parameter list precisely: `fileID` (source node), `depth` (0, 1, or 2 —
"0-2 hop traversal"), `edgeTypeFilter` (constrain which `EdgeType` values are traversed —
built on 3.1.4's `ValidEdgeType`/`EdgeTypeName`/`ParseEdgeType`), `maxNodes` (the cap on
returned result size, caller-supplied — the `k + 2k` system-wide value itself is a
query-agent concern, out of scope here per LLD's "Interactions with other modules"
section naming `query-agent` as `GraphNeighbors`' consumer, not this subtask).

## Design question resolved: compacted-only vs. merge-in-uncompacted EdgeLog entries

Resolved: **compacted-only** (reads only `*CSRGraph`, i.e. `graph.dat` as loaded by
`LoadCSR`/held in memory after `BuildCSR`/`Compact`). No merge of not-yet-compacted
`EdgeLog` entries. Evidence:

1. `docs/LLD/graph.md`'s "Traversal API" section sits directly under "## Storage layout",
   which introduces `graph.dat` as "CSR-like compact adjacency arrays per source fileID,
   with periodic compaction" — the traversal API description never once mentions
   `EdgeLog`, only ever appears in context of the compacted array.
2. `edgelog.go`'s own doc comments (`ReadNode`, `AppendEdge`) repeatedly describe `EdgeLog`
   as "not a general query API: no filtering, indexing, or cross-node lookup is provided"
   and explicitly scope multi-hop/filtered lookup to "compaction, 3.1.3, and the traversal
   API, 3.1.5" — read in context, this describes capabilities *the package as a whole*
   eventually provides (compaction turns the log into a queryable array; traversal queries
   that array), not a mandate that `GraphNeighbors` itself must read `EdgeLog` records
   directly. `EdgeLog` has no fileID->fileID cross-node enumeration primitive at all (only
   per-node `ReadNode`), which would make a straightforward "read-your-writes" merge
   expensive/awkward (it would need to enumerate every node's log to find inbound edges,
   or at minimum re-run a per-hop lookup against every log directory touched by BFS
   expansion — no such index exists).
3. The issue's own test spec text ("build graph... assert traversal result size capped...
   respects type filter") describes exercising a *built* graph, consistent with the
   existing test-fixture idiom every prior subtask in this package uses
   (`BuildCSR`/`WriteCSR`/`LoadCSR`), not a fixture that also seeds unflushed `EdgeLog`
   entries.
4. Architectural precedent from 3.1.3/3.1.4: `docs/LLD/graph.md`'s "Crash-safety ordering"
   section for compaction explicitly designates the **post-rename `graph.dat` as
   authoritative and durable**, treating not-yet-truncated log entries as a best-effort,
   accepted-risk gap rather than something later readers are expected to reconcile against.
   Extending that same "graph.dat is authoritative" posture to the read path is the
   conservative, consistent choice, and avoids a whole new class of consistency bugs
   (lock ordering between `EdgeLog`'s per-node writers and a traversal read) in a package
   that has already had 2 severe bugs at exactly this kind of seam (3.1.3's two fix
   cycles).
5. Nothing in the issue text or LLD says "read-your-writes" or "uncompacted" or
   "freshness" — the absence of any such requirement, combined with (1)-(4) above,
   is treated as evidence *against* the merge design, not silence to fill in by
   assumption (per the task's explicit instruction not to assume either way without
   evidence).

Consequence: freshly-appended-but-not-yet-compacted edges are invisible to
`GraphNeighbors` until the next `Compact` run. This is an accepted, documented staleness
window (mirroring the same "post-rename graph.dat is authoritative" posture already
established for compaction's own crash-safety story), not a correctness bug.
