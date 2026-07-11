# Architecture Discovery — Subtask 5.3.1

## Token order followed
`.cdr/index/*` -> `docs/HLD.md` -> `docs/LLD/eval.md` -> touched-file precedents
(`agents/eval/ground_truth.py`, `agents/eval/baselines/{vector_rag,vector_rag_rerank,graphrag_lite}.py`,
their test files) -> new source written.

## docs/HLD.md (relevant excerpt)
Section "Stack decisions": benchmark corpus is enterprise-style support-ticket + synthetic
policy/manual PDFs with ~30-50 ground-truth cross-topic-reference topics. Three retrieval arms
compared (HiveMind, classic vector RAG, simplified GraphRAG). No numpy/pandas anywhere in
`agents/` stack decisions.

## docs/LLD/eval.md (relevant excerpt)
"`agents/eval/` — retrieval quality benchmark ... Topic-level recall/precision@k [and]
LLM-judge spot-check [and] per-stage $/1000-query [metrics, tracked across] corpus-growth
checkpoints (20%/50%/100%)." All three arms (vector RAG, vector-RAG+rerank, GraphRAG-lite)
"share an identical final-answer LLM" and are otherwise interchangeable at the retrieval-result
level. Known risk explicitly named: "graph traversal context blow-up — `agents/eval/` metrics
explicitly check whether graph expansion hurts precision at any corpus-growth checkpoint, not
just whether it helps recall" — i.e. precision@k (not just recall@k) is a first-class,
explicitly load-bearing metric for this project, not an afterthought.

## Ground-truth schema (`agents/eval/ground_truth.py`, subtask 5.1.3, already merged)
- `RelevantDoc(doc_id: str, label: RelevanceLabel)` where `RelevanceLabel` is `"primary"` or
  `"cross_reference"` (`_VALID_LABELS`).
- `QueryLabel(query: str, topic_id: str, relevant_docs: list[RelevantDoc])` — one per seeded
  topic, auto-derived from `manifest.json`.
- `GroundTruthDataset(source_manifest, topics: list[TopicGroundTruth], queries: list[QueryLabel])`
  is the on-disk shape of `data/synthetic_corpus/ground_truth.json` (confirmed by reading the
  live file: 32 `queries`, each with a `relevant_docs` list of `{doc_id, label}` dicts, `label`
  exactly `"primary"` or `"cross_reference"`).
- `load_ground_truth(path)` parses this file back into a `GroundTruthDataset`.

## Existing recall/precision helpers (the duplication this subtask must resolve)

1. `agents/eval/baselines/vector_rag.py::recall_at_k(retrieved_doc_ids, relevant_doc_ids, k)`
   (subtask 5.2.1, already merged). Signature: `(list[str], set[str], int) -> float`. Semantics:
   fraction of `relevant_doc_ids` present in `retrieved_doc_ids[:k]`; vacuously `1.0` if
   `relevant_doc_ids` is empty. Consumed by:
   - `agents/eval/test_vector_rag_baseline.py` (direct unit tests + fixture-corpus integration
     test).
   - `agents/eval/test_graphrag_baseline.py` (imports `recall_at_k` from `vector_rag`, per its
     own established-precedent comment: "does not reimplement `recall_at_k`/`precision_at_k`;
     own test file imports both directly from `vector_rag`/`vector_rag_rerank` for
     consistency").
   - `vector_rag.py`'s own `select_chunk_config` grid-search (internal use).

2. `agents/eval/baselines/vector_rag_rerank.py::precision_at_k(retrieved_doc_ids,
   relevant_doc_ids, k)` (subtask 5.2.2, already merged). Signature: `(list[str], set[str],
   int) -> float`. Semantics: fraction of `retrieved_doc_ids[:k]` that are relevant; `1.0` if
   `k <= 0` (vacuous, "no slots to get wrong" — explicitly documented as mirroring
   `recall_at_k`'s vacuous-case *style* but for the precision denominator); `0.0` if the top-k
   slice is empty. Consumed by `agents/eval/test_vector_rag_rerank.py` (direct unit tests +
   fixture integration test).

3. `graphrag_lite.py` (5.2.3, already merged) deliberately does **not** define its own
   recall/precision helper — its docstring explicitly defers to a "future metrics pipeline
   (issue #28, not yet built)" that should let "all baseline arms" be treated "uniformly." This
   is direct, in-repo confirmation that this subtask (5.3.1 under issue #28) is the intended
   consolidation point.

## Shared arm output contract
`vector_rag.retrieve_documents`, `vector_rag_rerank.retrieve_documents_reranked`, and
`graphrag_lite.retrieve_documents` all return the same shape: `list[str]` of document ids,
ranked best-first, truncated to `top_k`. This subtask's new query/arm-level scoring helpers can
therefore accept that exact shape from any of the three arms without arm-specific branching.

## Resolution decided (non-breaking refactor, per standing constraint #3 in the task spec)
`agents/eval/metrics.py` becomes the single canonical home for `recall_at_k` and
`precision_at_k` (byte-identical semantics/docstrings to the two existing implementations, so
no existing test's expected values change), plus new ground-truth-aware helpers
(`relevant_doc_id_set`, `score_query`, `score_arm`) that consume `ground_truth.RelevantDoc` /
`QueryLabel` objects directly and reuse the two base functions. `vector_rag.py`'s `recall_at_k`
and `vector_rag_rerank.py`'s `precision_at_k` become thin re-exports (`from eval.metrics import
recall_at_k` / `precision_at_k`) — not reimplementations — so every existing import site
(`test_vector_rag_baseline.py`, `test_vector_rag_rerank.py`, `test_graphrag_baseline.py`,
`vector_rag.py`'s own `select_chunk_config`) keeps working unmodified, with zero behavior change
and zero duplication going forward.

## Primary vs cross_reference simplification (documented, deliberate)
Per the task's own guidance ("keep it simple; document any deliberate simplification"): the new
`score_query`/`score_arm` helpers take an `include_cross_reference: bool = True` flag that
either counts every `RelevantDoc` (primary + cross_reference) as relevant (default — matches
`docs/LLD/eval.md`'s "Topic-level recall/precision@k" framing, which does not itself distinguish
strengths), or restricts to `primary`-only when the caller explicitly wants the stricter
"only the document this topic actually documents" measurement (useful for the LLD's disclosed
"does graph expansion hurt precision" risk check, since a naive all-labels-count-as-relevant
precision score would flatter graph-expansion-style retrieval that surfaces true
cross-references). This subtask does not wire either mode into a specific benchmark run
(reserved for a future corpus-wiring subtask, e.g. 5.3.4, per `vector_rag.py`'s and
`graphrag_lite.py`'s own established "wiring to `datasets.py`/`ground_truth.py` real corpus is
out of scope for this subtask" precedent) — it only ships both computation and a fixture-based
correctness test for each.

## Test-file precedent
`agents/eval/test_vector_rag_baseline.py` / `test_vector_rag_rerank.py` / `test_graphrag_baseline.py`
all use plain hand-computed fixture `list[str]` / `set[str]` values for their pure-unit
recall/precision tests (no network, always run) — this subtask's
`test_metrics_recall_precision.py` follows the same pure-unit style, plus adds fixture
`RelevantDoc`/`QueryLabel` objects (matching the real ground-truth schema) for the new
ground-truth-aware helpers.
