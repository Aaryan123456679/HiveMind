"""Topic-level recall/precision@k computation against ground truth (issue #28, subtask 5.3.1).

Per issue #28 (milestone #7, "Phase 5"), subtask 5.3.1's acceptance criteria: "Given retrieval
results and ground-truth labels, recall/precision@k is computed correctly for each arm."
`docs/LLD/eval.md` names "Topic-level recall/precision@k" as one of the retrieval-quality
metrics `agents/eval/` tracks across corpus-growth checkpoints, alongside an LLM-judge
spot-check and per-stage cost -- this module covers the recall/precision half only (pure
computation, no LLM calls, per this subtask's own standing constraints).

Canonical home for `recall_at_k` / `precision_at_k` -- duplication resolved
----------------------------------------------------------------------------
Two of the three already-merged baseline-arm subtasks each shipped their own copy of one of
these two metrics, scoped narrowly to their own module's needs at the time:

- `agents/eval/baselines/vector_rag.py` (subtask 5.2.1) shipped `recall_at_k`, consumed by its
  own tests, `agents/eval/test_graphrag_baseline.py` (imports it directly, per that test file's
  own "established precedent for reuse" comment), and this module's grid-search helper.
- `agents/eval/baselines/vector_rag_rerank.py` (subtask 5.2.2) shipped `precision_at_k`,
  consumed by its own tests.
- `agents/eval/baselines/graphrag_lite.py` (subtask 5.2.3) deliberately shipped **neither**,
  with its own docstring explicitly deferring to "a future metrics pipeline (issue #28, not yet
  built)" so that "all baseline arms" can be treated "uniformly" -- i.e. this exact module.

This module is now the single canonical implementation of both functions (byte-identical
semantics/docstrings to the two pre-existing copies, so no existing test's expected values
change). `vector_rag.py` and `vector_rag_rerank.py` are updated to *re-export* (`from
eval.metrics import recall_at_k` / `precision_at_k`) rather than reimplement, so every existing
import site keeps working unmodified -- a non-breaking refactor, not a parallel duplicate (per
this subtask's own standing guidance: since 5.2.1-5.2.4 are already merged, avoid regression
risk to their passing tests).

Uniform "arm" contract
-----------------------
`vector_rag.retrieve_documents`, `vector_rag_rerank.retrieve_documents_reranked`, and
`graphrag_lite.retrieve_documents` all return the same shape: a ranked, best-first,
`top_k`-truncated `list[str]` of document ids. `score_query`/`score_arm` below accept exactly
that shape, so any of the three arms (or a future fourth) can be scored the same way without
arm-specific branching.

Primary vs. cross_reference relevance -- deliberate simplification, disclosed
--------------------------------------------------------------------------------
`agents/eval/ground_truth.py` (subtask 5.1.3) labels each `RelevantDoc` as either `"primary"`
(the document a topic is actually about) or `"cross_reference"` (a document that deliberately
references the topic in passing). `docs/LLD/eval.md`'s "Topic-level recall/precision@k" framing
does not itself distinguish relevance strength, so this module's default
(`include_cross_reference=True`) counts both labels as relevant -- the simple, LLD-literal
reading. Callers that want the stricter "only the document this topic is actually about"
measurement (useful for the LLD's disclosed "does graph expansion hurt precision" risk check,
since counting every cross-reference as relevant would flatter graph-expansion-style retrieval
that surfaces true cross-references as if they were exact hits) can pass
`include_cross_reference=False`. Wiring either mode into an actual benchmark run against the
real corpus is out of scope for this subtask (reserved for a future corpus-wiring subtask, e.g.
5.3.4, mirroring 5.2.1/5.2.2/5.2.3's own disclosed "fixture-only, not yet wired to
datasets.py/ground_truth.py's real corpus" scope boundary) -- this subtask only ships the
computation plus a fixture-based correctness test for both modes.
"""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field

from eval.ground_truth import QueryLabel, RelevantDoc


def recall_at_k(retrieved_doc_ids: list[str], relevant_doc_ids: set[str], k: int) -> float:
    """Fraction of `relevant_doc_ids` present in the top `k` of `retrieved_doc_ids`.

    Returns `1.0` if `relevant_doc_ids` is empty (vacuously satisfied -- no relevant docs to
    miss), matching the standard IR-metric convention for an empty ground-truth set.

    Canonical implementation -- see this module's docstring, "Canonical home for
    `recall_at_k`/`precision_at_k`" section. `agents/eval/baselines/vector_rag.py::recall_at_k`
    re-exports this exact function.
    """
    if not relevant_doc_ids:
        return 1.0
    top_k_ids = set(retrieved_doc_ids[:k])
    hits = len(top_k_ids & relevant_doc_ids)
    return hits / len(relevant_doc_ids)


def precision_at_k(retrieved_doc_ids: list[str], relevant_doc_ids: set[str], k: int) -> float:
    """Fraction of the top `k` `retrieved_doc_ids` that are in `relevant_doc_ids`.

    Returns `1.0` if `k <= 0` (vacuously satisfied -- no slots to get wrong), matching
    `recall_at_k`'s vacuous-case convention in style but for the precision denominator.
    Returns `0.0` if the top-k slice is empty (`k > 0` but nothing was retrieved).

    Canonical implementation -- see this module's docstring, "Canonical home for
    `recall_at_k`/`precision_at_k`" section.
    `agents/eval/baselines/vector_rag_rerank.py::precision_at_k` re-exports this exact function.
    """
    if k <= 0:
        return 1.0
    top_k_ids = retrieved_doc_ids[:k]
    if not top_k_ids:
        return 0.0
    hits = sum(1 for doc_id in top_k_ids if doc_id in relevant_doc_ids)
    return hits / len(top_k_ids)


def relevant_doc_id_set(
    relevant_docs: Iterable[RelevantDoc], *, include_cross_reference: bool = True
) -> set[str]:
    """Extract a plain `doc_id` set from `ground_truth.RelevantDoc` objects.

    See this module's docstring, "Primary vs. cross_reference relevance" section, for the
    `include_cross_reference` simplification this implements.

    Args:
        relevant_docs: `RelevantDoc` objects (e.g. `QueryLabel.relevant_docs`).
        include_cross_reference: If `True` (default), both `"primary"`- and
            `"cross_reference"`-labeled docs count as relevant. If `False`, only
            `"primary"`-labeled docs count.

    Returns:
        A set of `doc_id` strings.
    """
    if include_cross_reference:
        return {doc.doc_id for doc in relevant_docs}
    return {doc.doc_id for doc in relevant_docs if doc.label == "primary"}


@dataclass(frozen=True)
class QueryScore:
    """Recall/precision@k for one query against its ground-truth relevant-doc labels."""

    query: str
    topic_id: str
    recall: float
    precision: float


def score_query(
    retrieved_doc_ids: list[str],
    query_label: QueryLabel,
    k: int,
    *,
    include_cross_reference: bool = True,
) -> QueryScore:
    """Score one arm's retrieval result for one query against its ground-truth label.

    Args:
        retrieved_doc_ids: The arm's ranked, best-first, `top_k`-truncated doc-id list for
            `query_label.query` (matching `retrieve_documents`'s shared output shape across all
            three baseline arms).
        query_label: The `ground_truth.QueryLabel` (topic + relevant docs) this query is scored
            against.
        k: The cutoff for both recall@k and precision@k.
        include_cross_reference: See `relevant_doc_id_set`.

    Returns:
        A `QueryScore` with both metrics computed at cutoff `k`.
    """
    relevant_ids = relevant_doc_id_set(
        query_label.relevant_docs, include_cross_reference=include_cross_reference
    )
    return QueryScore(
        query=query_label.query,
        topic_id=query_label.topic_id,
        recall=recall_at_k(retrieved_doc_ids, relevant_ids, k),
        precision=precision_at_k(retrieved_doc_ids, relevant_ids, k),
    )


@dataclass(frozen=True)
class ArmScore:
    """Recall/precision@k for one retrieval arm across a full query set.

    `arm_name` is a free-form label (e.g. `"vector_rag"`, `"vector_rag_rerank"`,
    `"graphrag_lite"`, `"hivemind"`) -- this module does not hardcode arm identities, since all
    three (or four) arms share the same `list[str]` retrieval-result shape and are scored
    identically.
    """

    arm_name: str
    k: int
    per_query: list[QueryScore] = field(default_factory=list)

    @property
    def mean_recall(self) -> float:
        """Mean recall@k across `per_query`. `0.0` if `per_query` is empty."""
        if not self.per_query:
            return 0.0
        return sum(score.recall for score in self.per_query) / len(self.per_query)

    @property
    def mean_precision(self) -> float:
        """Mean precision@k across `per_query`. `0.0` if `per_query` is empty."""
        if not self.per_query:
            return 0.0
        return sum(score.precision for score in self.per_query) / len(self.per_query)


def score_arm(
    arm_name: str,
    retrieved_by_query: Mapping[str, list[str]],
    queries: list[QueryLabel],
    k: int,
    *,
    include_cross_reference: bool = True,
) -> ArmScore:
    """Score one retrieval arm's results against `queries`' ground-truth labels at cutoff `k`.

    Args:
        arm_name: A free-form label identifying which retrieval arm produced
            `retrieved_by_query` (e.g. `"vector_rag"`).
        retrieved_by_query: Maps each `QueryLabel.query` string to that arm's ranked,
            best-first, `top_k`-truncated doc-id list (matching `retrieve_documents`'s shared
            output shape across all baseline arms). A query present in `queries` but absent
            from this mapping is scored as an empty retrieval (recall/precision computed
            against `[]`), not an error -- this keeps `score_arm` usable even when an arm
            fails to return results for some queries.
        queries: The ground-truth `QueryLabel` list to score against (e.g.
            `ground_truth.GroundTruthDataset.queries`).
        k: The cutoff for both recall@k and precision@k, applied uniformly to every query.
        include_cross_reference: See `relevant_doc_id_set`.

    Returns:
        An `ArmScore` with one `QueryScore` per entry in `queries`, in the same order.
    """
    per_query = [
        score_query(
            retrieved_by_query.get(query_label.query, []),
            query_label,
            k,
            include_cross_reference=include_cross_reference,
        )
        for query_label in queries
    ]
    return ArmScore(arm_name=arm_name, k=k, per_query=per_query)
