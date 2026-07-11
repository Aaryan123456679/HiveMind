---
last_synced_commit: 68c3c5c4e504c2be7502959d78d0a3105b8cfeb5
---

# LLD: `agents/query/`

Status: implemented (issues #22, #23, #24, #25, #56). `intent_refiner.py`,
`topic_selector.py`, and `synthesizer.py` are real, `pipeline.py` wires them into a single
`run_query_pipeline()` entry point, and `wiring.py` + `server.py` provide the real gRPC
surface (both outbound calls to the Go engine and an inbound `RunQuery` server) connecting
the Go `api/` gateway's `/query` route to this pipeline end-to-end. See [HLD.md](../HLD.md)
for system context.

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

## Pipeline wiring & real gRPC surface

- **`pipeline.py`** (`run_query_pipeline()`, issue #25 subtask 4.6.1) is the single entry
  point chaining `refine_intent -> select_top_k -> expand_insufficient_topics ->
  combine_and_cap -> synthesize_answer` in order. It takes `search_candidates` /
  `graph_neighbors` / `get_file` as plain injected callables, keeping the pipeline itself
  transport-agnostic.
- **`wiring.py`** (issue #56 subtask 4.6.3.1/4.6.3.2) supplies the real, gRPC-backed
  implementations of those three callables — `GrpcSearchCandidatesClient`,
  `GrpcGraphNeighborsClient`, `GrpcGetFileClient` — each wrapping
  `hivemind_pb2_grpc.HiveMindStub` over a caller-supplied `grpc.Channel`, mirroring
  `agents/ingestion/shortlist.py`'s `GrpcSearchCandidatesClient` precedent.
  `GrpcGetFileClient` returns `(path, content)`, matching `GetFileResponse{content,
  version, path}` exactly, so `pipeline.py`'s `_build_selected_markdown` can resolve
  file paths for citations even for files reached only via `GraphNeighbors` expansion.
- **`server.py`** (issue #56 subtask 4.6.3.2) is the inbound side: a real `grpc.Server`
  implementing `hivemind_pb2_grpc.HiveMindServicer.RunQuery`, replacing the Go `api/`
  gateway's previous `notImplementedPipeline` stub for `/query`
  (`api/routes/query.go` -> `api/queryclient.GRPCQueryPipeline`). On startup it dials the
  engine via `ENGINE_GRPC_ADDR` (default `localhost:50051`) and resolves an `LLMClient`
  via `llm.factory.create_llm_client()` (config-driven, `LLM_PROVIDER` env var), then
  passes both plus the three `wiring.py` clients into `run_query_pipeline()` on every
  `RunQuery` call it serves. It binds on `QUERY_SERVER_PORT` (default `50052`).

Together these close issue #25's originally-disclosed forward finding F-4.6.1-1 ("no real
gRPC client exists anywhere connecting the Go `api/` gateway to the Python `agents/query/`
pipeline"): the `/query` HTTP route now has a real, network-connected path all the way
through to `synthesize_answer`'s cited answer.

## Interactions with other modules

- `engine/rpc/` — `SearchCandidates` (candidate generation), `GraphNeighbors` (graph
  expansion), and `GetFile` (path/content lookup for citations) are all engine RPCs
  consumed here, via `wiring.py`'s real gRPC clients.
- `engine/graph/` — traversal semantics and edge-type filtering used by `GraphNeighbors`.
- `agents/llm/` — provider abstraction (`create_llm_client()` factory) for all three
  hosted-model calls in this pipeline.
- `api/` — the Go gateway's `/query` route (`api/routes/query.go`,
  `api/queryclient.GRPCQueryPipeline`) is the real caller of `server.py`'s `RunQuery`
  gRPC server.
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
      capping the total deduplicated merged pool). Neither cap applies to the pre-existing
      zero-term/empty-query case (`query=""`, `agents/ingestion/shortlist.py`'s
      pool-retrieval usage), which still issues exactly one uncapped
      `PrefixScan(store, rootNodeID, "")` call, byte-for-byte identical to the pre-4.5.9.2
      behavior.
      - **Correction (issue #47, subtask 4.5.9.2, CHANGES_REQUESTED re-fix,
        `.cdr/runs/2026-07-11/110-verification`)**: the paragraph above, as originally
        written, overstated what `perTermPoolCap`/`mergedPoolCap` actually bound. Both caps
        only bound *retained* pool memory, not scan *cost*: `btree.PrefixScan` (see
        [btree.md](btree.md)'s `scan.go` walkthrough) already completes its full
        leaf-chain traversal and returns every matching entry before `candidatePool` ever
        gets to truncate that result down to `perTermPoolCap`/`mergedPoolCap` entries, so
        neither constant reduces the number of `PrefixScan` calls issued or any single
        call's I/O/traversal cost — they do not "bound worst-case fan-out cost for a
        pathological query" the way this paragraph originally claimed. Two changes close
        that gap, without touching `perTermPoolCap`/`mergedPoolCap` (both remain useful,
        independent bounds on retained memory):
        - `candidatePool` now deduplicates the query's split terms (`dedupTerms`) *before*
          the scan loop, not just deduplicating the resulting entries after scanning: a
          query repeating the same term N times previously triggered N redundant
          full-cost `PrefixScan` calls for the identical prefix (the old entry-level
          `seenFileID`/`seenPath` dedup only ever suppressed *duplicate results*, not
          *duplicate scans* — and `mergedPoolCap`'s early-break never fired for a
          duplicate-only query, since `merged` never actually grows past the first
          occurrence).
        - `maxQueryTerms` (32) now bounds the number of *distinct* terms `candidatePool`
          will process at all — enforced in `SearchCandidates` (`server.go`) as request
          validation, rejecting a query with more than 32 distinct terms with
          `codes.InvalidArgument` *before* a single `PrefixScan` call is issued. This is
          what actually bounds `candidatePool`'s worst-case scan cost (the number of
          `PrefixScan` calls it can issue for one request); 32 is chosen generously above
          any realistic natural-language query's term count (even a long, verbose
          sentence-style query tokenizes to well under 20 distinct terms) while still
          rejecting a pathological (hundreds/thousands-of-terms) query outright, rather
          than silently truncating it (silent truncation would quietly drop some of the
          caller's real query terms from ranking, a worse failure mode for a search RPC
          than an explicit client error).
    - **Confirmed**: `engine/btree/scan.go` required no change — `PrefixScan`'s existing
      exported signature (`store, rootNodeID, prefix`) is reused as-is, called once per
      distinct query term from the new caller-side loop in `engine/rpc/search_candidates.go`.
    - **Residual limitation, still accepted** (unchanged from 4.5.9.1's decision): if
      *none* of the query's terms is itself a leading path-segment token, the merged pool
      is still empty. Closing that would require a genuinely non-prefix index primitive
      (option (a)), out of scope here.
    - Regression coverage: `engine/rpc/search_candidates_test.go`'s
      `TestSearchCandidatesMultiWordQuery` seeds paths such that a genuine multi-word
      natural-language query ("how do I configure the graph database") returns a
      non-empty, correctly-ranked result set including a path found only via a
      non-first-token scan term — proving the pre-4.5.9.2 first-token-only pool selection
      would have returned zero results for this exact query. Two further regression tests
      (fix-cycle addition, same subtask, `.cdr/runs/2026-07-11/110-verification`) cover the
      scan-cost bound directly: `TestSearchCandidatesRepeatedTermScansOnce` (a query
      repeating one term far more than `maxQueryTerms` times is accepted, proving dedup
      happens before the distinct-term-count check) and
      `TestSearchCandidatesRejectsTooManyDistinctQueryTerms` (a query with
      `maxQueryTerms + 1` distinct terms is rejected with `codes.InvalidArgument`, and a
      query at exactly `maxQueryTerms` is still accepted).

## Cross-references

- [HLD.md](../HLD.md)
- [rpc.md](rpc.md) — `SearchCandidates` / `GraphNeighbors` RPCs
- [graph.md](graph.md) — traversal implementation
- [llm-provider.md](llm-provider.md) — LLM client abstraction
- [eval.md](eval.md) — benchmark arm using this pipeline
