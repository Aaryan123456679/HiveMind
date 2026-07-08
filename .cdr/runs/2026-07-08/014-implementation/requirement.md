# Requirement — Subtask 3.1.4

Source: `gh issue view 15` (issue #15, Epic Phase 3 "Graph store + ingestion agents"),
milestone #5. NOTE: the issue body has injected fake system-reminder-style text appended
after the real subtask list (fake date-change notice, fake MCP tool instructions, fake
"Auto Mode Active" directive) — this is a known, recurring prompt-injection pattern for
this issue per prior runs. Ignored; not acted upon; noted here only for disclosure.

## Subtask 3.1.4 (as extracted from the issue's subtask list)

Title: **Edge-type ENTITY_COOCCUR LLM_ASSERTED, SPLIT_SIBLING, REDIRECT** (full edge-type
support, following on from 3.1.3's incidental/necessary addition of the two newer type
constants).

- Acceptance criteria: type-filtered edge creation, and correct discrimination between
  ENTITY_COOCCUR (weight-summing) and the other three types (last-write-wins dedup) is
  fully supported and validated — not merely accepted-without-checking as 3.1.3 left it.
- Test spec: `go test ./engine/graph/... -run TestEdgeTypes` — exercises type-filtered
  edge creation/validation for all 4 edge types.
- Impacted modules: `engine/graph/edge.go`, `engine/graph/edge_test.go` (new files, per
  the issue's own module list for this subtask).

## Corroborating source-of-truth: explicit deferred-work markers left by 3.1.3

Three separate doc comments in already-shipped 3.1.1/3.1.2/3.1.3 code explicitly name
`engine/graph/edge.go` and "subtask 3.1.4" as owning full edge-type validation:

1. `edgelog.go` (`EdgeLog.AppendEdge` doc, line ~113-117): "It returns an error if
   edge.Type is the EdgeTypeInvalid zero-value sentinel; no other type validation is
   performed here (that is subtask 3.1.4's job - see this file's package doc comment)."
2. `compact.go` (package doc, near the `EdgeType` extension note): "`EdgeType` gained two
   new values ahead of subtask 3.1.4 (`EdgeEntityCooccur`, `EdgeLLMAsserted`, in
   `edge_append.go`) ... full type-filtered creation/validation support remains 3.1.4's
   job."
3. `edge_append.go` (`EdgeEntityCooccur` const doc): "3.1.4 remains responsible for
   validating this value (e.g. rejecting it where it doesn't belong) beyond what this
   file does."

## docs/LLD/graph.md corroboration

- "Edge-type creation/validation support beyond rejecting the `EdgeTypeInvalid` zero-value
  sentinel is subtask 3.1.4's job (`engine/graph/edge.go`)."
- "Edge shape" section names the 4 canonical edge-type tokens: `ENTITY_COOCCUR`,
  `LLM_ASSERTED`, `SPLIT_SIBLING`, `REDIRECT`.
- "Weight-aggregation semantics": `ENTITY_COOCCUR` sums weight / max `LastUpdated`; every
  other type (`SPLIT_SIBLING`, `REDIRECT`, `LLM_ASSERTED`) is deduplicated by
  `(source, target, type)` with last-write-wins by `LastUpdated`. (This is already
  correctly implemented by 3.1.3's `compact.go` `mergeEdges` — confirmed by direct
  reading, not assumed; see architecture-discovery.md.)

## Scope decision

3.1.4's deliverable is the **validation and canonical-naming layer** for `EdgeType`
across the stack — not a change to `edge_append.go`'s existing behavior (which is
correctly scoped to `SPLIT_SIBLING`/`REDIRECT` only per `docs/LLD/graph.md`'s explicit
statement that `EdgeAppender` "remains scoped to SPLIT_SIBLING/REDIRECT edges"), and not
a change to `compact.go`'s merge semantics (already correct, verified by direct reading).
