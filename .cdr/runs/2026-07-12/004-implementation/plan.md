# Plan — Subtask 5.4.1

1. Create `agents/eval/traversal_precision.py`:
   - `NO_EXPANSION_MAX_HOPS = 0` constant.
   - `retrieve_for_expansion_arms(query, graph, llm_client, *, top_k, expanded_max_hops, model)`
     -> `(expansion_doc_ids, no_expansion_doc_ids)`, both via `graphrag_lite.retrieve_documents`.
   - `QueryPrecisionDelta` frozen dataclass: `query`, `topic_id`, `expansion_precision`,
     `no_expansion_precision`, `.delta` property, `.expansion_decreased_precision` property.
   - `TraversalPrecisionComparison` frozen dataclass: `checkpoint_label`, `expansion_score`
     (`ArmScore`), `no_expansion_score` (`ArmScore`), `per_query_deltas` (`list[QueryPrecisionDelta]`),
     `.decreased_queries` property, `.expansion_ever_hurt_precision` property.
   - `compare_traversal_precision(graph, queries, llm_client, *, top_k, k, expanded_max_hops=
     DEFAULT_MAX_HOPS, include_cross_reference=True, checkpoint_label="default", model=None)` ->
     `TraversalPrecisionComparison`. Runs both arms per query, scores both via `eval.metrics.
     score_arm`, builds per-query deltas.
   - `CorpusGrowthCheckpoint` frozen dataclass: `label`, `docs` (`list[tuple[str, str]]`) --
     generic checkpoint representation (no real-corpus wiring).
   - `compare_precision_across_checkpoints(checkpoints, queries, llm_client, *, top_k, k, ...)`
     -> `list[TraversalPrecisionComparison]`: builds one `EntityGraph` per checkpoint, delegates
     to `compare_traversal_precision` per checkpoint.
   - `checkpoints_with_precision_decrease(comparisons)` -> filters to comparisons where
     `.expansion_ever_hurt_precision` is True -- the "reports any checkpoint where expansion
     decreases precision" acceptance criterion.
   - Full module docstring disclosing: issue/subtask provenance, reuse-not-reimplementation of
     metrics.py/graphrag_lite.py, Ollama-only/offline scope, corpus-growth-checkpoint deferred-
     wiring scope boundary (mirrors 5.2.3's own disclosed pattern).

2. Create `agents/eval/test_traversal_precision_check.py`:
   - In-file deterministic `_StubLLMClient(LLMClient)` (no network).
   - Fixture corpus: 2 docs designed so a query's directly-matched entity co-occurs (1 hop) with
     a low-relevance neighbor entity that itself maps to an irrelevant document -- i.e. expansion
     (`max_hops=1`) pulls in an extra irrelevant doc that no-expansion (`max_hops=0`) does not,
     lowering precision@k for the expansion arm on that query.
   - Ground truth (`QueryLabel`) marks only the direct-match doc as relevant.
   - Test: `compare_traversal_precision(...)` on this fixture with `k` set so the added neighbor
     doc lands inside the cutoff -> assert `result.expansion_ever_hurt_precision is True` and
     the specific query appears in `result.decreased_queries` with `expansion_precision <
     no_expansion_precision`.
   - Additional smaller unit tests: `QueryPrecisionDelta.delta`/`.expansion_decreased_precision`
     correctness; a "no decrease" control case (expansion adds nothing extra, or adds only
     relevant docs) to prove the flag is not always-true; `checkpoints_with_precision_decrease`
     over a 2-checkpoint list (one flagged, one clean).

3. Run `pytest agents/eval/test_traversal_precision_check.py -v` plus the full `agents/eval/`
   suite (excluding any live-Ollama-skippable tests, which naturally skip offline) to confirm no
   regression.

4. Self-consistency check, one commit, handoff.
