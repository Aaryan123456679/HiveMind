# Requirement — Subtask 5.3.1 (issue #28, milestone #7 / Phase 5)

**Title**: Topic-level recall/precision@k computation against ground truth

**Acceptance criteria**: Given retrieval results and ground-truth labels, recall/precision@k is
computed correctly for each arm.

**Test spec**: `pytest agents/eval/test_metrics_recall_precision.py`: compute against a
hand-verified fixture ground-truth set, assert exact expected values.

**Impacted modules (per issue)**: `agents/eval/metrics.py`, `agents/eval/test_metrics_recall_precision.py`.

**Constraints**:
1. No LLM calls — pure computation over given inputs.
2. No new dependency — `agents/pyproject.toml` untouched.
3. Standard CDR workflow, one local commit, no push, no self-verification.

**Known duplication to resolve** (discovered during architecture-discovery, confirmed before
planning): `agents/eval/baselines/vector_rag.py` already ships `recall_at_k` (5.2.1) and
`agents/eval/baselines/vector_rag_rerank.py` already ships `precision_at_k` (5.2.2), both
consumed by already-merged, already-passing tests (`test_vector_rag_baseline.py`,
`test_vector_rag_rerank.py`, `test_graphrag_baseline.py`). `graphrag_lite.py`'s own docstring
explicitly foreshadows this subtask: "module not reimplement recall_at_k/precision_at_k; own
test file imports directly ... for consistency ... so future metrics pipeline (issue #28, not
yet built) can treat all baseline arms uniformly." This subtask makes `agents/eval/metrics.py`
the single canonical home for both functions, with `vector_rag.py`/`vector_rag_rerank.py`
re-exporting (not reimplementing) from it — a non-breaking refactor that keeps all existing
import sites and tests green.
