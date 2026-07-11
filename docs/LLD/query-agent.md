---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `agents/query/`

Status: scaffold only (`agents/query/__init__.py` empty). See [HLD.md](../HLD.md) for system
context.

## Purpose

Query-time retrieval pipeline: refine intent, select and expand relevant topics via the graph, and
synthesize a final answer with citations. Three components, each a hosted-small-model agent (see
[llm-provider.md](llm-provider.md)).

## `intent_refiner.py`

Input: raw query + short history.
Output: `{ refined_intent, entities: [], query_type }`.

## `topic_selector.py`

- Receives a candidate topic list from a non-LLM Go-side `SearchCandidates` call (see
  [rpc.md](rpc.md)).
- Selects the top-`k` topics, where `k` is a tunable hyperparameter (default 3).
- For any topic it judges insufficient alone, it may also request graph-traversal expansion
  (0-2 hops); the Go engine performs the expansion via `GraphNeighbors` (see [graph.md](graph.md)).
- The combined result is **hard-capped at `k + 2k` total files** to prevent context blow-up — this
  is a system-wide invariant, not just an implementation detail (see
  [HLD.md](../HLD.md#7-system-wide-known-risks)).

## `synthesizer.py`

Final LLM call: refined intent + concatenated selected markdown (with file-path headers) -> answer
with inline file-path citations.

## Pipeline order

```
query -> intent_refiner -> topic_selector (+ SearchCandidates / GraphNeighbors) -> synthesizer -> answer
```

## Interactions with other modules

- `engine/rpc/` — `SearchCandidates` (candidate generation) and `GraphNeighbors` (graph
  expansion) are both engine RPCs consumed here.
- `engine/graph/` — traversal semantics and edge-type filtering used by `GraphNeighbors`.
- `agents/llm/` — provider abstraction for all three hosted-model calls in this pipeline.
- `agents/eval/` — the query pipeline is one of the arms benchmarked (HiveMind arm vs. vector-RAG
  vs. GraphRAG-style baseline).

## Known risks

- **Graph traversal context blow-up** — mitigated by the `k + 2k` hard cap; the benchmark suite
  must measure whether traversal ever *hurts* precision (not just whether it helps recall). See
  [eval.md](eval.md).
- **`SearchCandidates`' candidate pool is only as good as `btree.PrefixScan`'s literal-prefix
  matching — multi-word natural-language queries need an explicit strategy.** Flagged as a
  `design_limitation` (non-blocking) during task 4.2.1 (issue #21, commit `b8ebc64`,
  `.cdr/index/regression.jsonl`; also `.cdr/memory/pending.md`'s top entry): `search_candidates.go`
  delegates pool selection entirely to a single `PrefixScan` on the query's first
  whitespace-separated token, so a query like "how do I configure the graph database"
  prefix-scans on "how" and returns zero candidates before term-overlap ranking ever runs.
  Confirmed backward-compatible with `agents/ingestion/shortlist.py` (always calls with
  `query=""`), but a real gap for this package's `topic_selector.py` once it is wired to a real
  `SearchCandidates` call with genuine natural-language queries.
  - As of this writing, `topic_selector.py` (issue #23, subtask 4.4.1-4.4.3) does **not** itself
    call `SearchCandidates` yet — confirmed by reading the module directly: `select_top_k()`
    takes an already-decoded `Sequence[TopicCandidate]`, and `SearchCandidatesFn` exists only as
    a documented, unused injection-point type alias for a future caller. The real gRPC/HTTP
    wiring that will hand `topic_selector.py` a live `SearchCandidates` result for a real user
    query is issue #56's in-flight scope (`api/` + `agents/query/pipeline.py` wiring
    `search_candidates`/`graph_neighbors`/`get_file` callables to replace
    `notImplementedPipeline`). This decision is therefore recorded now so it is available before
    or alongside #56's wiring lands, rather than after real multi-word queries are already
    silently returning empty pools in production.
  - **Decision (issue #47, subtask 4.5.9.1)**: of the three options considered —
    (a) extend `engine/btree` with a non-prefix query primitive (e.g. token-set intersection),
    (b) have the caller issue multiple single-term `PrefixScan`s and merge/re-rank client-side, or
    (c) formally accept and document the prefix-only limitation as-is —
    **option (b) is chosen**. `engine/rpc/search_candidates.go` will tokenize the query into its
    whitespace-separated terms (reusing the same term-splitting convention
    `tokenizeTerms`/`rankCandidates` already use for scoring), issue one `btree.PrefixScan` per
    term, and merge the resulting `ScanEntry` pools (deduplicated by `FileID`/`Path`, preserving
    each entry's first-seen order) into a single candidate pool before handing it to the
    existing, unmodified `rankCandidates` term-overlap scorer. Rationale:
    - `rankCandidates`'s `termOverlapScore` already scores each candidate against the *full*
      query term set regardless of which `PrefixScan` call produced it, so merging multiple
      scan pools requires no change to ranking logic — only to pool assembly.
    - `PrefixScan` is already a read-only, lock-free, optimistic-concurrency operation (see
      [btree.md](btree.md#concurrency)); issuing one scan per query term is a small, bounded
      fan-out (one call per distinct term in a realistic natural-language query) with no new
      locking or correctness risk to the B+Tree itself.
    - It keeps the fix scoped to the RPC layer (`engine/rpc/`), leaving `engine/btree`'s core
      scan primitive and its latch-crabbing/optimistic-read invariants completely untouched —
      smaller blast radius than option (a), and directly addresses the gap unlike option (c).
    - **Residual limitation, still accepted**: if *none* of the query's whitespace-separated
      terms is itself a leading path-segment token (e.g. a query entirely of stopwords not
      present in any indexed path prefix), the merged pool is still empty. Closing that
      remaining gap would require a genuinely non-prefix index primitive (option (a)) or a
      different index structure entirely; that is explicitly out of scope for this decision and
      remains a disclosed, non-blocking limitation.
    - Implementation (not part of this decision-only subtask) is deferred to subtask 4.5.9.2:
      impacted modules `engine/rpc/search_candidates.go`, `engine/rpc/search_candidates_test.go`
      (new `TestSearchCandidatesMultiWordQuery`), and `engine/btree/scan.go` (expected to remain
      behaviorally unchanged — `PrefixScan`'s existing exported signature is reused as-is, called
      multiple times by the caller; 4.5.9.2 should confirm no change to `scan.go` is actually
      needed before closing that impacted-module entry).

## Cross-references

- [HLD.md](../HLD.md)
- [rpc.md](rpc.md) — `SearchCandidates` / `GraphNeighbors` RPCs
- [graph.md](graph.md) — traversal implementation
- [llm-provider.md](llm-provider.md) — LLM client abstraction
- [eval.md](eval.md) — benchmark arm using this pipeline
