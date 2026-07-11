"""Tests for `agents/eval/traversal_precision.py` (issue #29, subtask 5.4.1).

Per this subtask's own test spec: "fixture scenario where expansion adds a low-relevance
neighbor, assert the comparison correctly flags a precision decrease." Fully offline: a
deterministic in-file `_StubLLMClient` (no network, no live Ollama/OpenRouter/Gemini call)
stands in for entity extraction, mirroring `test_graphrag_baseline.py`'s own established
stub-client pattern (a separate, file-local instance here, not a shared import, matching that
test file's own "no cross-test-file coupling" precedent).
"""

from __future__ import annotations

from eval.baselines.graphrag_lite import EntityGraph
from eval.ground_truth import QueryLabel, RelevantDoc
from eval.traversal_precision import (
    CorpusGrowthCheckpoint,
    QueryPrecisionDelta,
    checkpoints_with_precision_decrease,
    compare_precision_across_checkpoints,
    compare_traversal_precision,
)
from llm.client import LLMClient


class _StubLLMClient(LLMClient):
    """Deterministic canned-response `LLMClient` stub for entity extraction. See module
    docstring; mirrors `test_graphrag_baseline.py::_StubLLMClient`'s substring-keyed pattern."""

    def __init__(self, responses: dict[str, str]) -> None:
        self._responses = responses

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        for key, response in self._responses.items():
            if key in prompt:
                return response
        raise AssertionError(f"_StubLLMClient: no canned response configured for prompt: {prompt!r}")


# --- Fixture: expansion adds a low-relevance neighbor ---
#
# doc-a mentions entities X and Y (co-occurrence edge X-Y). doc-b mentions Y and Z (co-occurrence
# edge Y-Z). A query about "X" direct-matches only doc-a (entity_to_docs["x"] == {"doc-a"}).
# With 1-hop expansion, the query also reaches "y" (a co-occurring neighbor of "x"), and "y" is
# itself linked to doc-b -- a document with no relevance to the query at all. So expansion pulls
# in doc-b as a low-relevance neighbor; no-expansion (max_hops=0) never does.


def test_expansion_low_relevance_neighbor_flags_precision_decrease():
    stub = _StubLLMClient(
        {
            "doc-a-text": '["X", "Y"]',
            "doc-b-text": '["Y", "Z"]',
            "about-x-query": '["X"]',
        }
    )
    docs = [("doc-a", "doc-a-text content"), ("doc-b", "doc-b-text content")]
    graph = EntityGraph.build(docs, stub)

    query_label = QueryLabel(
        query="about-x-query",
        topic_id="topic-x",
        relevant_docs=[RelevantDoc(doc_id="doc-a", label="primary")],
    )

    comparison = compare_traversal_precision(
        graph,
        [query_label],
        stub,
        top_k=2,
        k=2,
        expanded_max_hops=1,
        checkpoint_label="fixture-checkpoint",
    )

    # No-expansion (direct match only) retrieves only doc-a -- perfect precision.
    assert comparison.no_expansion_score.per_query[0].precision == 1.0
    # Expansion (1 hop) additionally pulls in doc-b via the shared "y" neighbor -- precision
    # drops because doc-b is not relevant to this query.
    assert comparison.expansion_score.per_query[0].precision == 0.5

    assert comparison.expansion_ever_hurt_precision is True
    assert len(comparison.decreased_queries) == 1
    decreased = comparison.decreased_queries[0]
    assert decreased.query == "about-x-query"
    assert decreased.topic_id == "topic-x"
    assert decreased.expansion_precision < decreased.no_expansion_precision
    assert decreased.delta == -0.5


def test_no_decrease_when_expansion_adds_nothing_extra():
    """Control case: a query whose direct-match entity has no other-document neighbor at all
    (no co-occurrence edges leaving it) -- expansion has nothing to add, so precision cannot
    drop, proving the flag is not a hard-coded always-true."""
    stub = _StubLLMClient(
        {
            "doc-a-text": '["X"]',
            "isolated-x-query": '["X"]',
        }
    )
    docs = [("doc-a", "doc-a-text content")]
    graph = EntityGraph.build(docs, stub)

    query_label = QueryLabel(
        query="isolated-x-query",
        topic_id="topic-x",
        relevant_docs=[RelevantDoc(doc_id="doc-a", label="primary")],
    )

    comparison = compare_traversal_precision(
        graph,
        [query_label],
        stub,
        top_k=2,
        k=2,
        expanded_max_hops=1,
        checkpoint_label="fixture-no-decrease",
    )

    assert comparison.expansion_score.per_query[0].precision == 1.0
    assert comparison.no_expansion_score.per_query[0].precision == 1.0
    assert comparison.expansion_ever_hurt_precision is False
    assert comparison.decreased_queries == []


def test_query_precision_delta_properties():
    decreased = QueryPrecisionDelta(
        query="q1", topic_id="t1", expansion_precision=0.5, no_expansion_precision=1.0
    )
    assert decreased.delta == -0.5
    assert decreased.expansion_decreased_precision is True

    unchanged = QueryPrecisionDelta(
        query="q2", topic_id="t2", expansion_precision=1.0, no_expansion_precision=1.0
    )
    assert unchanged.delta == 0.0
    assert unchanged.expansion_decreased_precision is False

    improved = QueryPrecisionDelta(
        query="q3", topic_id="t3", expansion_precision=1.0, no_expansion_precision=0.5
    )
    assert improved.delta == 0.5
    assert improved.expansion_decreased_precision is False


def test_checkpoints_with_precision_decrease_filters_correctly():
    # Checkpoint 1: the low-relevance-neighbor fixture (expansion hurts precision).
    hurt_stub = _StubLLMClient(
        {
            "doc-a-text": '["X", "Y"]',
            "doc-b-text": '["Y", "Z"]',
            "about-x-query": '["X"]',
        }
    )
    hurt_query = QueryLabel(
        query="about-x-query",
        topic_id="topic-x",
        relevant_docs=[RelevantDoc(doc_id="doc-a", label="primary")],
    )

    # Checkpoint 2: the isolated-entity fixture (expansion has nothing to add, no decrease).
    clean_stub = _StubLLMClient(
        {
            "doc-a-text": '["X"]',
            "isolated-x-query": '["X"]',
        }
    )
    clean_query = QueryLabel(
        query="isolated-x-query",
        topic_id="topic-x",
        relevant_docs=[RelevantDoc(doc_id="doc-a", label="primary")],
    )

    checkpoints = [
        CorpusGrowthCheckpoint(
            label="checkpoint-hurt",
            docs=[("doc-a", "doc-a-text content"), ("doc-b", "doc-b-text content")],
        ),
    ]
    hurt_comparisons = compare_precision_across_checkpoints(
        checkpoints, [hurt_query], hurt_stub, top_k=2, k=2, expanded_max_hops=1
    )

    clean_checkpoints = [
        CorpusGrowthCheckpoint(label="checkpoint-clean", docs=[("doc-a", "doc-a-text content")]),
    ]
    clean_comparisons = compare_precision_across_checkpoints(
        clean_checkpoints, [clean_query], clean_stub, top_k=2, k=2, expanded_max_hops=1
    )

    all_comparisons = hurt_comparisons + clean_comparisons
    flagged = checkpoints_with_precision_decrease(all_comparisons)

    assert len(flagged) == 1
    assert flagged[0].checkpoint_label == "checkpoint-hurt"
