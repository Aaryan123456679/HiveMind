"""Tests for `agents/eval/metrics.py` (issue #28, subtask 5.3.1).

Per subtask 5.3.1's test spec: "compute against a hand-verified fixture ground-truth set, assert
exact expected values." Every fixture below is small enough to hand-verify by inspection; every
expected value in this file was computed by hand (not derived from running the code first) and
is asserted exactly (`==`), not with a tolerance -- matching the acceptance criterion's "computed
correctly" wording.

Pure computation only -- no network, no LLM calls, no skip-if-unreachable gating anywhere in
this file (unlike `test_vector_rag_baseline.py`/`test_vector_rag_rerank.py`/
`test_graphrag_baseline.py`, which each have a live-local-model tier; `eval.metrics` has none,
per this subtask's own standing constraint that it is pure math over given inputs).
"""

from __future__ import annotations

from eval.baselines import vector_rag, vector_rag_rerank
from eval.ground_truth import QueryLabel, RelevantDoc
from eval.metrics import (
    ArmScore,
    QueryScore,
    precision_at_k,
    recall_at_k,
    relevant_doc_id_set,
    score_arm,
    score_query,
)

# --- recall_at_k: hand-computed fixture values ---


def test_recall_at_k_matches_hand_computed_fixture():
    # 2 of 2 relevant docs retrieved within top 3 -> 2/2 = 1.0
    assert recall_at_k(["a", "b", "c"], {"a", "b"}, k=3) == 1.0
    # 1 of 2 relevant docs retrieved within top 3 -> 1/2 = 0.5
    assert recall_at_k(["a", "x", "y"], {"a", "b"}, k=3) == 0.5
    # 0 of 2 relevant docs retrieved within top 3 -> 0/2 = 0.0
    assert recall_at_k(["x", "y", "z"], {"a", "b"}, k=3) == 0.0
    # 1 of 3 relevant docs retrieved within top 2 -> 1/3
    assert recall_at_k(["a", "x", "b", "c"], {"a", "b", "c"}, k=2) == 1 / 3


def test_recall_at_k_empty_relevant_set_is_vacuous():
    assert recall_at_k(["a", "b"], set(), k=2) == 1.0
    assert recall_at_k([], set(), k=5) == 1.0


def test_recall_at_k_respects_k_cutoff():
    # "a" is relevant but sits at index 1; k=1 only looks at ["x"], so it's missed.
    assert recall_at_k(["x", "a"], {"a"}, k=1) == 0.0
    assert recall_at_k(["x", "a"], {"a"}, k=2) == 1.0


# --- precision_at_k: hand-computed fixture values ---


def test_precision_at_k_matches_hand_computed_fixture():
    # Both of top-2 are relevant -> 2/2 = 1.0
    assert precision_at_k(["a", "b"], {"a", "b"}, k=2) == 1.0
    # 1 of top-2 is relevant -> 1/2 = 0.5
    assert precision_at_k(["a", "b"], {"a"}, k=2) == 0.5
    # 0 of top-2 is relevant -> 0/2 = 0.0
    assert precision_at_k(["a", "b"], {"c"}, k=2) == 0.0
    # 2 of top-3 relevant -> 2/3
    assert precision_at_k(["a", "x", "b"], {"a", "b"}, k=3) == 2 / 3


def test_precision_at_k_zero_k_is_vacuous():
    assert precision_at_k(["a"], {"a"}, k=0) == 1.0
    assert precision_at_k(["a"], {"a"}, k=-1) == 1.0


def test_precision_at_k_empty_retrieved_list():
    assert precision_at_k([], {"a"}, k=1) == 0.0


def test_precision_at_k_only_considers_top_k():
    # "a" relevant but at index 1; k=1 only looks at ["b"], so precision@1 is 0.
    assert precision_at_k(["b", "a"], {"a"}, k=1) == 0.0


# --- relevant_doc_id_set: primary vs. cross_reference simplification ---

_TOPIC_RELEVANT_DOCS = [
    RelevantDoc(doc_id="doc-primary-1", label="primary"),
    RelevantDoc(doc_id="doc-xref-1", label="cross_reference"),
    RelevantDoc(doc_id="doc-xref-2", label="cross_reference"),
]


def test_relevant_doc_id_set_include_cross_reference():
    result = relevant_doc_id_set(_TOPIC_RELEVANT_DOCS, include_cross_reference=True)
    assert result == {"doc-primary-1", "doc-xref-1", "doc-xref-2"}


def test_relevant_doc_id_set_primary_only():
    result = relevant_doc_id_set(_TOPIC_RELEVANT_DOCS, include_cross_reference=False)
    assert result == {"doc-primary-1"}


def test_relevant_doc_id_set_default_is_include_cross_reference():
    assert relevant_doc_id_set(_TOPIC_RELEVANT_DOCS) == relevant_doc_id_set(
        _TOPIC_RELEVANT_DOCS, include_cross_reference=True
    )


# --- score_query: single-query recall+precision against a QueryLabel ---

# Mirrors the real ground_truth.json shape (see architecture-discovery.md): one QueryLabel with
# one primary doc and two cross-reference docs.
_QUERY_LABEL = QueryLabel(
    query="What is the policy on Data Retention Policy?",
    topic_id="data-retention",
    relevant_docs=[
        RelevantDoc(doc_id="doc-data-retention", label="primary"),
        RelevantDoc(doc_id="doc-mobile-device-management", label="cross_reference"),
        RelevantDoc(doc_id="doc-supplier-diversity", label="cross_reference"),
    ],
)


def test_score_query_against_fixture_query_label_include_cross_reference():
    # Retrieved top-3: 2 of 3 relevant docs present (data-retention, mobile-device-management),
    # supplier-diversity missing. recall = 2/3, precision = 2/3 (both hits within top 3).
    retrieved = ["doc-data-retention", "doc-mobile-device-management", "doc-unrelated"]
    result = score_query(retrieved, _QUERY_LABEL, k=3)
    assert result == QueryScore(
        query=_QUERY_LABEL.query,
        topic_id=_QUERY_LABEL.topic_id,
        recall=2 / 3,
        precision=2 / 3,
    )


def test_score_query_against_fixture_query_label_primary_only():
    # Primary-only relevant set is just {"doc-data-retention"}; it's retrieved at rank 1.
    # recall = 1/1 = 1.0, precision@3 = 1/3 (only 1 of top-3 slots is the primary doc).
    retrieved = ["doc-data-retention", "doc-mobile-device-management", "doc-unrelated"]
    result = score_query(retrieved, _QUERY_LABEL, k=3, include_cross_reference=False)
    assert result.recall == 1.0
    assert result.precision == 1 / 3


# --- score_arm: mean recall/precision across a full query set, one arm ---

_QUERY_A = QueryLabel(
    query="query-a", topic_id="topic-a", relevant_docs=[RelevantDoc("doc-a1", "primary")]
)
_QUERY_B = QueryLabel(
    query="query-b",
    topic_id="topic-b",
    relevant_docs=[
        RelevantDoc("doc-b1", "primary"),
        RelevantDoc("doc-b2", "cross_reference"),
    ],
)
_QUERIES = [_QUERY_A, _QUERY_B]


def test_score_arm_computes_mean_recall_and_precision():
    # query-a: retrieved ["doc-a1", "doc-x"] at k=2 -> recall 1/1=1.0, precision 1/2=0.5
    # query-b: retrieved ["doc-x", "doc-b1"] at k=2 -> recall 1/2=0.5, precision 1/2=0.5
    retrieved_by_query = {
        "query-a": ["doc-a1", "doc-x"],
        "query-b": ["doc-x", "doc-b1"],
    }
    result = score_arm("fixture_arm", retrieved_by_query, _QUERIES, k=2)
    assert isinstance(result, ArmScore)
    assert result.arm_name == "fixture_arm"
    assert result.k == 2
    assert len(result.per_query) == 2
    assert result.per_query[0].recall == 1.0
    assert result.per_query[0].precision == 0.5
    assert result.per_query[1].recall == 0.5
    assert result.per_query[1].precision == 0.5
    # Mean recall = (1.0 + 0.5) / 2 = 0.75; mean precision = (0.5 + 0.5) / 2 = 0.5
    assert result.mean_recall == 0.75
    assert result.mean_precision == 0.5


def test_score_arm_distinguishes_three_arms():
    # Same ground truth, three distinct (fixture-standin) arm result sets, per the shared
    # `retrieve_documents(...) -> list[str]` output contract all three real baseline arms use.
    arm_results = {
        "vector_rag": {"query-a": ["doc-a1"], "query-b": ["doc-b1", "doc-b2"]},
        "vector_rag_rerank": {"query-a": ["doc-x"], "query-b": ["doc-b1", "doc-x"]},
        "graphrag_lite": {"query-a": ["doc-a1"], "query-b": ["doc-x", "doc-x2"]},
    }
    scores = {
        arm_name: score_arm(arm_name, results, _QUERIES, k=2)
        for arm_name, results in arm_results.items()
    }

    # vector_rag: query-a recall 1.0, query-b recall 2/2=1.0 -> mean recall 1.0
    assert scores["vector_rag"].mean_recall == 1.0
    # vector_rag_rerank: query-a recall 0.0, query-b recall 1/2=0.5 -> mean recall 0.25
    assert scores["vector_rag_rerank"].mean_recall == 0.25
    # graphrag_lite: query-a recall 1.0, query-b recall 0/2=0.0 -> mean recall 0.5
    assert scores["graphrag_lite"].mean_recall == 0.5


def test_score_arm_missing_query_scored_as_empty():
    # "query-b" absent from retrieved_by_query -> scored as retrieving [], not a KeyError.
    retrieved_by_query = {"query-a": ["doc-a1"]}
    result = score_arm("partial_arm", retrieved_by_query, _QUERIES, k=2)
    assert result.per_query[0].recall == 1.0
    assert result.per_query[1].recall == 0.0
    assert result.per_query[1].precision == 0.0


# --- Duplication resolution: vector_rag.py / vector_rag_rerank.py re-export, not reimplement ---


def test_vector_rag_recall_at_k_reexports_metrics():
    assert vector_rag.recall_at_k is recall_at_k


def test_vector_rag_rerank_precision_at_k_reexports_metrics():
    assert vector_rag_rerank.precision_at_k is precision_at_k
