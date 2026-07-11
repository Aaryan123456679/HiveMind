# task-5.3.1: Topic-level recall/precision@k scoring against ground truth (issue #28)

## Summary

**Problem:** Issue #28 (milestone #7 cross-arm benchmark comparison) needs a single, canonical way to score any retrieval arm's ranked doc-id results against `ground_truth.py`'s per-topic/per-query relevant-doc labels. `recall_at_k` (introduced in 5.2.1's `vector_rag.py`) and `precision_at_k` (introduced in 5.2.2's `vector_rag_rerank.py`) had each been duplicated ad hoc inside their own baseline module, and 5.2.3's `graphrag_lite.py` explicitly deferred building the shared metrics pipeline these functions belong in. Without consolidation, the three benchmark arms (HiveMind, vector-RAG, GraphRAG-lite) could not be scored uniformly ahead of the real cross-arm comparison work later in this milestone.

**Solution:** `agents/eval/metrics.py` is now the single canonical home for `recall_at_k`/`precision_at_k` (semantics unchanged), plus new ground-truth-aware helpers (`relevant_doc_id_set`, `score_query`, `score_arm`) that consume `ground_truth.RelevantDoc`/`QueryLabel` directly and work uniformly across all three baseline arms' shared `list[str]` retrieval-result shape. `agents/eval/baselines/vector_rag.py` and `vector_rag_rerank.py` now re-export (not reimplement) from `metrics.py`, so every existing import site and test keeps working unmodified — confirmed genuine re-export (not a parallel copy) during verification. `agents/eval/test_metrics_recall_precision.py` adds fixture coverage for the new module, including re-export identity checks.

Files touched: `agents/eval/metrics.py` (new), `agents/eval/test_metrics_recall_precision.py` (new), `agents/eval/baselines/vector_rag.py` and `vector_rag_rerank.py` (refactored to re-export). No production behavior change to the two pre-existing scoring functions; no new dependency (`agents/pyproject.toml` untouched); no LLM calls introduced.

## Impact

- Establishes the shared scoring layer (`metrics.py`) that issue #28's later subtasks (through 5.3.4) will wire directly to real corpus retrieval output from all three benchmark arms, without any arm needing its own private copy of recall/precision math.
- Zero regressions: full `agents/eval/` suite (105 tests) passes; `ruff check` clean on both new files; `pyproject.toml` diff empty across both commits (no new dependency).
- Two low-severity, non-blocking findings from verification, logged in `.cdr/index/regression.jsonl`:
  - **F1** (edge_case_test_gap): `recall_at_k` dedupes retrieved doc ids via `set()` while `precision_at_k` does not, so a duplicate doc id in the retrieved top-k list counts as multiple hits for precision but only once for recall. This asymmetry is mathematically defensible (standard per-slot-precision vs. per-distinct-doc-recall convention) and is pre-existing behavior carried over unchanged from 5.2.1/5.2.2 — not a new bug introduced by this refactor — but no fixture in the test suite exercises it. Recommended before 5.3.4 wires this to a real corpus where duplicates could plausibly leak through.
  - **F2** (documentation_accuracy): the implementation commit message and module docstring claim "19 hand-verified fixture assertions"; an independent reproduction of the test collection shows 17 test functions (40 individual assert statements) in `test_metrics_recall_precision.py`. Cosmetic miscount only, does not affect correctness of the module or its tests.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10021-verification`
- Commits verified: `7f513df123144fc76aab4a440545b18127953b32` (feat), `39bd2dd9988d0654995eb425010ef696a0bcea2c` (chore: run bookkeeping only, no source change)
- Zero blocking findings. All required dimensions (requirements conformance, architecture conformance, math correctness, re-export genuineness, regression risk, include/cross-reference semantics, no-new-dependency, security, performance, maintainability) independently confirmed PASS; two low-severity comments (F1, F2 above) drove the overall verdict to PASS_WITH_COMMENTS. Confidence: high.
- Independently reproduced: `test_metrics_recall_precision.py` 17 passed; combined `vector_rag`/`vector_rag_rerank`/`graphrag_lite` baseline suites 51 passed; full `agents/eval/` suite 105 passed; `ruff check` clean; `pyproject.toml` diff empty (no new dependency).

## Release Notes

- Added `agents/eval/metrics.py` as the single canonical home for topic-level recall@k/precision@k scoring against ground-truth relevant-doc labels, with new ground-truth-aware helpers (`relevant_doc_id_set`, `score_query`, `score_arm`) usable uniformly across all three milestone #7 benchmark arms.
- `vector_rag.py`/`vector_rag_rerank.py` now re-export these functions instead of each maintaining its own duplicate copy; no behavior change, no new dependency.
- First of four subtasks under issue #28 (5.3.1-5.3.4); issue remains open pending the remaining subtasks.
