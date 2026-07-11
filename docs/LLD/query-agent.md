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
  - **Implemented (issue #47, subtask 4.5.9.2)**: option (b) is now implemented in
    `engine/rpc/search_candidates.go`'s new `candidatePool` function, called once from
    `SearchCandidates` (`server.go`) in place of the old single-first-token
    `btree.PrefixScan` call. Two gaps flagged by 4.5.9.1's own verification
    (`.cdr/index/regression.jsonl`, run `101-verification`) were resolved as part of this
    implementation, not left as further-deferred follow-ups:
    - **Tokenization-convention consistency** (the decision text above said
      "whitespace-separated terms," but `rankCandidates` actually splits on any
      non-alphanumeric run via `termSplitRE`/`tokenizeTerms`, which differs for
      punctuated/hyphenated queries such as "graph-database"): `candidatePool` splits the
      query via a new `splitTerms` helper that shares the *exact same* `termSplitRE` regex
      `tokenizeTerms` uses (`tokenizeTerms` is now simply
      `splitTerms(strings.ToLower(s))`) — pool assembly and ranking now use one single
      splitting convention, not two divergent ones. `splitTerms` deliberately does **not**
      lower-case its output, unlike `tokenizeTerms`: `btree.PrefixScan`'s prefix match is
      case-sensitive against on-disk paths that preserve their original case, so feeding it
      a lower-cased term (as a naive reuse of `tokenizeTerms` would have) could silently
      drop real mixed-case-path matches. Ranking (`termOverlapScore`, via `rankCandidates`'s
      still-unmodified call to `tokenizeTerms`) remains case-insensitive, which is correct
      for its in-memory string comparison against already-lower-cased path tokens.
    - **Perf/unbounded-pool bound**: `btree.PrefixScan` has no per-call result limit, and
      `SearchCandidates`'s `max_results` is applied only to the final *ranked* output
      (after pool assembly, per `server.go`'s own doc comment) — so merging N per-term
      scans without any earlier bound could multiply an already-uncapped single-scan cost
      by the query's term count. `candidatePool` now applies two bounds, both documented
      inline in `search_candidates.go`: `perTermPoolCap` (500 entries, truncating any single
      term's own `PrefixScan` result before merging) and `mergedPoolCap` (2000 entries,
      capping the total deduplicated merged pool). Both are deliberately conservative
      constants chosen to be far above any realistic single-term or natural-language
      multi-term query's expected result size, so they should not visibly change behavior
      in the common case.
      Neither cap applies to the pre-existing zero-term/empty-query case (`query=""`,
      `agents/ingestion/shortlist.py`'s pool-retrieval usage), which still issues exactly
      one uncapped `PrefixScan(store, rootNodeID, "")` call, byte-for-byte identical to the
      pre-4.5.9.2 behavior.
      - **Correction (issue #47, subtask 4.5.9.2, CHANGES_REQUESTED re-fix,
        `.cdr/runs/2026-07-11/110-verification`)**: the text above previously claimed
        `perTermPoolCap`/`mergedPoolCap` bound "worst-case fan-out cost for a pathological
        query" — that overstated what they do. `btree.PrefixScan`
        (`engine/btree/scan.go`'s leaf-chain-following scan) always completes its **full**
        traversal and returns every matching entry before `candidatePool` ever gets to
        truncate the result to either cap, so both caps bound only the merged pool's
        *retained memory*, not the *cost* (I/O, traversal work) of the `PrefixScan` calls
        that produced it. Two real gaps existed: (1) nothing de-duplicated repeated terms,
        so a query repeating one term N times issued N redundant full-cost scans (the
        `mergedPoolCap` early-break could not catch this, since a repeat contributes zero
        *new* entries to the deduplicated merge, so `merged`'s length never grows from a
        repeat); (2) nothing bounded the *number* of distinct terms `candidatePool`'s loop
        processes, so a query with an arbitrarily large distinct-term count could still
        force an arbitrarily large number of full-cost `PrefixScan` calls. Both are now
        fixed in `search_candidates.go`: `dedupTerms` de-duplicates `candidatePool`'s term
        list before the scan loop (a repeated term is scanned once, not N times), and
        `maxQueryTerms` (32) is a hard cap on the number of *distinct* terms a request may
        have, enforced via `validateQueryTermCount` in `SearchCandidates` (`server.go`,
        `codes.InvalidArgument`) **before** `candidatePool` issues a single `PrefixScan`
        call — this pair, not `perTermPoolCap`/`mergedPoolCap`, is what actually bounds
        worst-case scan cost now. 32 was chosen as generously above realistic
        natural-language query term counts (a handful up to a couple dozen words even for
        an unusually long query) while still rejecting a pathological query outright rather
        than silently truncating it (consistent with `max_results < 0` already being
        rejected rather than clamped). See `docs/LLD/btree.md`'s "Known risks" for the
        cross-referenced correction.
    - **Confirmed**: `engine/btree/scan.go` required no change — `PrefixScan`'s existing
      exported signature (`store, rootNodeID, prefix`) is reused as-is, called once per
      distinct query term from the new caller-side loop in
      `engine/rpc/search_candidates.go`.
    - **Residual limitation, still accepted** (unchanged from 4.5.9.1's decision): if
      *none* of the query's terms is itself a leading path-segment token, the merged pool
      is still empty. Closing that would require a genuinely non-prefix index primitive
      (option (a)), out of scope here.
    - Regression coverage: `engine/rpc/search_candidates_test.go`'s
      `TestSearchCandidatesMultiWordQuery` seeds paths such that a genuine multi-word
      natural-language query ("how do I configure the graph database") returns a
      non-empty, correctly-ranked result set including a path found only via a
      non-first-token scan term — proving the pre-4.5.9.2 first-token-only pool selection
      would have returned zero results for this exact query. Added for this correction:
      `TestDedupTermsCollapsesRepeatedTerms` (unit test on `dedupTerms` itself),
      `TestSearchCandidatesRepeatedTermScansOnce` (a query repeating one term far more times
      than `maxQueryTerms` allows distinct terms still succeeds, proving dedup happens
      before the distinct-term-count check), and
      `TestSearchCandidatesRejectsTooManyDistinctQueryTerms` (a query with
      `maxQueryTerms + 1` distinct terms is rejected with `codes.InvalidArgument`; exactly
      `maxQueryTerms` distinct terms is accepted).

## Cross-references

- [HLD.md](../HLD.md)
- [rpc.md](rpc.md) — `SearchCandidates` / `GraphNeighbors` RPCs
- [graph.md](graph.md) — traversal implementation
- [llm-provider.md](llm-provider.md) — LLM client abstraction
- [eval.md](eval.md) — benchmark arm using this pipeline
