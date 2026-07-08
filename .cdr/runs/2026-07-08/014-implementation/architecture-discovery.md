# Architecture Discovery — Subtask 3.1.4

## Index/handoff order followed
1. `.cdr/index/*.jsonl` (feature/task/regression indexes) — grepped for `graph`, `EdgeType`,
   `3.1.3` to locate the right prior handoffs before reading source.
2. `.cdr/commits/task-3.1.3.md` — full narrative of 3.1.3's two fix cycles (compaction
   retry-idempotency + segment-floor bugs), confirms compaction's merge logic itself
   (`mergeEdges`) was the part verified correct across 3 passes — the type-validation gap
   was explicitly and repeatedly left as this subtask's job, not silently forgotten.
3. `docs/LLD/graph.md` (full file) — canonical edge-type tokens, weight-aggregation
   semantics, module boundaries (`EdgeAppender` scoped to SPLIT_SIBLING/REDIRECT only;
   `EdgeLog`/CSR scoped to all 4 types).
4. Direct source reads (required — this subtask spans 4 files' interfaces, LLD alone is
   not enough to see the encode/decode boundaries): `edge_append.go`, `csr.go`,
   `edgelog.go`, `compact.go`.

## What already exists (from 3.1.1/3.1.2/3.1.3, confirmed by direct reading)

- `edge_append.go`: `EdgeType` enum with 4 constants (`EdgeSplitSibling`=1,
  `EdgeRedirect`=2 pre-existing since 2b.3.4/Phase 2b; `EdgeEntityCooccur`=3,
  `EdgeLLMAsserted`=4 added by 3.1.3 out of necessity for its own test spec).
  `decodeEdge`/`AppendEdge` in this file already correctly reject anything other than
  `EdgeSplitSibling`/`EdgeRedirect` — this is intentional per `docs/LLD/graph.md`
  ("`EdgeAppender`... remains scoped to `SPLIT_SIBLING`/`REDIRECT` edges written by
  `engine/split/execute.go`"). **Not a gap; left unchanged.**
- `csr.go`: `CSREdge{Target, Type, Weight, LastUpdated}` fixed-width encode/decode.
  `decodeCSREdge` performs **zero type validation** — any byte value round-trips
  silently, including values with no defined meaning (5, 200, etc.). `WriteCSR` also
  performs no type validation before encoding. **Genuine gap.**
- `edgelog.go`: `EdgeLog.AppendEdge` rejects only the `EdgeTypeInvalid` (0) sentinel —
  its own doc comment explicitly states "no other type validation is performed here
  (that is subtask 3.1.4's job)". Any nonzero byte, including undefined ones, is
  accepted and durably written. **Genuine gap, explicitly flagged by 3.1.3's author.**
- `compact.go`'s `mergeEdges`: dedup key is `(Target, Type)`; `EdgeEntityCooccur` sums
  `Weight` (max `LastUpdated`), every other type is last-write-wins by `LastUpdated`.
  Read directly and compared line-by-line against `docs/LLD/graph.md`'s
  "Weight-aggregation semantics" section — **already correct and already generalizes to
  all 4 types**, not hardcoded to assume only `ENTITY_COOCCUR` exists. This was verified
  three times in 3.1.3's fix cycle (008/010/012-verification) for the crash-safety
  surface; the merge logic itself was never the source of either bug found. **No change
  needed here.**

## What is genuinely new work for 3.1.4

1. A canonical validity check for `EdgeType` (`ValidEdgeType`/`(EdgeType).Valid()`)
   covering exactly the 4 defined values, to replace the `== EdgeTypeInvalid` spot-check
   in `edgelog.go` and add a check that does not currently exist at all in `csr.go`.
2. Canonical string-name mapping (`ENTITY_COOCCUR`, `LLM_ASSERTED`, `SPLIT_SIBLING`,
   `REDIRECT` — the exact tokens `docs/LLD/graph.md`'s "Edge shape" section names) plus a
   parse function, for "type-filtered ... creation" support and to give 3.1.5's
   `edgeTypeFilter` parameter (not in scope here, but consumes this) a canonical
   string<->`EdgeType` mapping to build on.
3. A validated `CSREdge` constructor (`NewCSREdge`) that rejects invalid/undefined
   `EdgeType` values at creation time — this is the "edge creation" half of "type-filtered
   creation/validation support" the LLD doc comment names.
4. Wire the new validation into `edgelog.go`'s `AppendEdge` (reject any undefined type,
   not just 0) and into `csr.go`'s decode path (`decodeCSREdge` becomes fallible; `LoadCSR`
   surfaces a decode error instead of silently accepting a garbage type byte) and encode
   path (`WriteCSR` rejects a `CSRGraph` containing an edge with an undefined type before
   ever writing bytes to disk).

## Files read directly (with the deferred-work markers each contains)
- `engine/graph/edge_append.go:59-75` (EdgeEntityCooccur/EdgeLLMAsserted const doc)
- `engine/graph/edgelog.go:113-121` (AppendEdge doc + implementation)
- `engine/graph/csr.go:73-102` (CSREdge encode/decode, no validation)
- `engine/graph/compact.go:1-95, 321-376` (package doc + mergeEdges)
- `docs/LLD/graph.md` (full file, ~125 lines)
