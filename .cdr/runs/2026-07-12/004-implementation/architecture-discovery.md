# Architecture Discovery — Subtask 5.4.1

Token order followed: `.cdr/index/*` -> `docs/HLD.md` + `docs/LLD/eval.md` -> touched-file
history (prior 5.3.1/5.2.3 modules) -> source read only for the specific reuse surfaces needed.

## docs/HLD.md (relevant excerpts)

- #7 "System-wide known risks": "Graph traversal context blow-up — bounded by a hard file-count
  cap of `k + 2k`; the benchmark must measure whether traversal ever hurts precision, not just
  recall." This is the exact risk subtask 5.4.1 closes the loop on.
- "Benchmark fairness" risk: vector-RAG baseline must be well-tuned (already addressed by prior
  subtasks, not this one).

## docs/LLD/eval.md (relevant excerpts)

- Metrics section: "Topic-level recall/precision@k" and "Corpus-growth-checkpoint degradation
  chart at 20%/50%/100% ingested — the key novelty result of the project."
- Known risks section: "the `agents/eval/` metrics must explicitly check whether graph expansion
  hurts precision at any corpus-growth checkpoint, not just whether it helps recall." — this is
  issue #29/subtask 5.4.1 verbatim.
- Confirms `agents/eval/pipeline.py`'s shared-final-LLM pattern (`run_hivemind_arm`,
  `run_vector_rag_arm`, `run_graphrag_lite_arm`) exists for full end-to-end arm runs, but this
  degradation-chart/checkpoint wiring against the real corpus is not yet built (scaffold-only
  status noted in the LLD header) — consistent with treating that wiring as future/deferred work
  and this subtask as the comparison-logic primitive only.

## Existing shared infrastructure read directly (reuse surfaces)

- `agents/eval/metrics.py` (5.3.1): `recall_at_k`, `precision_at_k`, `relevant_doc_id_set`,
  `QueryScore`, `score_query`, `ArmScore`, `score_arm`. `score_arm(arm_name, retrieved_by_query,
  queries, k, include_cross_reference=...)` returns an `ArmScore` with one `QueryScore` per query
  label, in order — exactly the shape needed to diff two arms' precision per query.
- `agents/eval/baselines/graphrag_lite.py` (5.2.3): `EntityGraph` (co-occurrence graph,
  `.build(docs, llm_client, model=...)`), `retrieve_documents(query, graph, llm_client, *,
  top_k, max_hops=DEFAULT_MAX_HOPS, model=None)`, `DEFAULT_MAX_HOPS = 1`. Module docstring
  explicitly documents `max_hops=0` as "disables hop expansion entirely (direct entity matches
  only)" — this is precisely the "no-expansion" arm subtask 5.4.1 needs; no new baseline logic
  required, only a comparison built on top of two calls to the same function.
- `agents/eval/ground_truth.py` (5.1.3): `QueryLabel` (`.query`, `.topic_id`, `.relevant_docs`)
  — the query-label shape `score_arm` consumes.
- `agents/eval/test_graphrag_baseline.py`: established `_StubLLMClient(LLMClient)` pattern
  (canned substring-keyed JSON responses, `complete()` override) for deterministic, offline,
  no-network entity-extraction stubbing — reused (a new stub instance, not the same class import,
  since it's file-local in that test module) rather than re-derived.

## Conclusion

No new baseline/metrics logic is needed. Subtask 5.4.1 is a comparison-and-reporting layer:
run `retrieve_documents` twice per query (once at the arm's real hop count, once forced to
`max_hops=0`), score both via `eval.metrics.score_arm`, and diff precision per query, flagging
any query (and, when checkpoint-batched, any checkpoint) where the expansion arm's precision is
strictly lower than the no-expansion arm's. This matches this project's established "canonical
home, no duplication" convention exactly (mirrors `metrics.py`'s own docstring precedent).
