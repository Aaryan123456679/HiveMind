# Plan — Subtask 5.3.1

1. Create `agents/eval/metrics.py`:
   - `recall_at_k(retrieved_doc_ids, relevant_doc_ids, k) -> float` — moved verbatim
     (semantics + docstring) from `vector_rag.py`.
   - `precision_at_k(retrieved_doc_ids, relevant_doc_ids, k) -> float` — moved verbatim
     (semantics + docstring) from `vector_rag_rerank.py`.
   - `relevant_doc_id_set(relevant_docs, *, include_cross_reference=True) -> set[str]` — extracts
     a plain doc-id set from a list of `ground_truth.RelevantDoc`, honoring the
     primary/cross_reference simplification flag.
   - `QueryScore` frozen dataclass (`query`, `topic_id`, `recall`, `precision`).
   - `score_query(retrieved_doc_ids, query_label, k, *, include_cross_reference=True) ->
     QueryScore` — single-query recall+precision against a `ground_truth.QueryLabel`.
   - `ArmScore` frozen dataclass (`arm_name`, `k`, `per_query: list[QueryScore]`) with
     `mean_recall`/`mean_precision` properties.
   - `score_arm(arm_name, retrieved_by_query, queries, k, *, include_cross_reference=True) ->
     ArmScore` — scores one arm's full query set uniformly (works for vector_rag,
     vector_rag_rerank, graphrag_lite, or any future arm, since all share the same `list[str]`
     retrieval output shape).
2. Edit `agents/eval/baselines/vector_rag.py`: replace the `recall_at_k` function body with an
   import re-export (`from eval.metrics import recall_at_k`), keep a one-line pointer docstring
   note. Remove the now-duplicated implementation.
3. Edit `agents/eval/baselines/vector_rag_rerank.py`: replace the `precision_at_k` function body
   with an import re-export (`from eval.metrics import precision_at_k`), keep a one-line
   pointer docstring note. Remove the now-duplicated implementation.
4. Create `agents/eval/test_metrics_recall_precision.py`:
   - Hand-verified fixture ground truth (small, exact -- computed by hand, asserted exactly) for
     `recall_at_k`/`precision_at_k` at several k values including edge cases (empty relevant
     set, k=0, partial overlap, k larger than retrieved list).
   - Fixture `QueryLabel`/`RelevantDoc` objects (mirroring the real `ground_truth.json` shape)
     to test `relevant_doc_id_set` (both `include_cross_reference` modes) and `score_query`.
   - A multi-query, multi-arm fixture (three different `retrieved_by_query` dicts standing in
     for the three real arms) to test `score_arm`'s mean recall/precision computation, with
     hand-computed expected means asserted exactly.
   - A regression test importing `recall_at_k` from both `eval.metrics` and
     `eval.baselines.vector_rag` and asserting they are the *same object* (`is`), and similarly
     for `precision_at_k` from `eval.baselines.vector_rag_rerank` -- proves the re-export, not a
     silent fork.
5. Run the full affected test set (`test_command` in impact-analysis.json) plus the whole
   `agents/eval/` suite's non-network-gated subset to confirm no regression.
6. Self-consistency check + one local commit (Problem/Solution/Impact style) + handoff.json.
