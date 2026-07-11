"""Tests for `query.pipeline.run_query_pipeline`, the full-chain wiring of
`intent_refiner -> topic_selector -> synthesizer`.

Per issue #25 subtask 4.6.1's test spec: the Python-side chain is tested with the LLM calls
mocked (via a small fake `LLMClient` subclass, mirroring `test_intent_refiner.py`'s /
`test_synthesizer.py`'s own `_FakeLLMClient` precedent) and `search_candidates`/
`graph_neighbors`/`get_file` supplied as plain fake callables that record call order and
arguments -- asserting correct call order and response shape end-to-end.
"""

from __future__ import annotations

import json

import pytest

from llm.client import LLMClient
from query.pipeline import PipelineError, QueryPipelineResult, run_query_pipeline
from query.topic_selector import GraphNeighbor, TopicCandidate


class _FakeLLMClient(LLMClient):
    """Fake `LLMClient` returning canned responses in call order, one per `complete()` call.

    Mirrors `test_synthesizer._FakeLLMClient`: real ABC subclass, records every call's
    prompt/kwargs for assertions.
    """

    def __init__(self, responses: list[str]) -> None:
        self._responses = list(responses)
        self.calls: list[dict] = []

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        self.calls.append(
            {
                "prompt": prompt,
                "model": model,
                "temperature": temperature,
                "max_tokens": max_tokens,
                "timeout": timeout,
            }
        )
        index = len(self.calls) - 1
        assert index < len(self._responses), "more LLM calls than canned responses supplied"
        return self._responses[index]


#: Canned intent-refinement JSON: a factual_lookup query about billing invoice disputes.
_INTENT_RESPONSE = json.dumps(
    {
        "refined_intent": "What is the status of the disputed invoice 4521?",
        "entities": ["invoice 4521"],
        "query_type": "factual_lookup",
    }
)

#: Canned synthesis JSON citing exactly the one file this pipeline test's fixture selects.
_SYNTHESIS_RESPONSE = json.dumps(
    {
        "answer": (
            "Invoice 4521 was disputed for a duplicate charge "
            "[billing/InvoiceDisputes.md]."
        ),
        "citations": ["billing/InvoiceDisputes.md"],
    }
)


def _make_candidates() -> list[TopicCandidate]:
    return [
        TopicCandidate(file_id=1, path="billing/InvoiceDisputes.md", score=0.9),
        TopicCandidate(file_id=2, path="billing/PaymentDelays.md", score=0.2),
    ]


#: `file_id -> content` only, matching `GetFileFn`'s real (post-4.6.3.1) shape -- path is no
#: longer part of this fixture's return value, mirroring `GetFileResponse`'s real shape
#: (`content`/`version`, no `path`). See `pipeline.py`'s `GetFileFn` proto-shape fix disclosure.
_FILE_CONTENT = {
    1: "Invoice 4521 was disputed for a duplicate charge.",
    2: "Payment for invoice 4521 was delayed.",
    3: "Refunds are issued within 5 business days.",
}


def test_run_query_pipeline_calls_agents_in_order() -> None:
    """Asserts the full chain is called in order: refine_intent's LLM call, then
    search_candidates, then graph_neighbors (only for the insufficient topic), then get_file
    (once per selected file_id), then synthesize_answer's LLM call.
    """
    call_log: list[tuple[str, tuple]] = []

    def fake_search_candidates(query: str, max_results: int) -> list[TopicCandidate]:
        call_log.append(("search_candidates", (query, max_results)))
        return _make_candidates()

    def fake_graph_neighbors(file_id: int, hops: int) -> list[GraphNeighbor]:
        call_log.append(("graph_neighbors", (file_id, hops)))
        return [GraphNeighbor(file_id=3, edge_type="ENTITY_COOCCUR", weight=1, hop=1)]

    def fake_get_file(file_id: int) -> str:
        call_log.append(("get_file", (file_id,)))
        return _FILE_CONTENT[file_id]

    llm_client = _FakeLLMClient([_INTENT_RESPONSE, _SYNTHESIS_RESPONSE])

    result = run_query_pipeline(
        "What's going on with invoice 4521?",
        [],
        llm_client=llm_client,
        search_candidates=fake_search_candidates,
        graph_neighbors=fake_graph_neighbors,
        get_file=fake_get_file,
        k=2,
    )

    assert isinstance(result, QueryPipelineResult)

    # First LLM call (intent refinement) must happen before search_candidates.
    assert len(llm_client.calls) == 2
    assert "What's going on with invoice 4521?" in llm_client.calls[0]["prompt"]

    names = [entry[0] for entry in call_log]
    assert names[0] == "search_candidates"
    # search_candidates was called with the *refined* intent, not the raw query.
    assert call_log[0][1][0] == "What is the status of the disputed invoice 4521?"

    # k=2 -> select_top_k keeps both fixture candidates (file_id=1, score=0.9 and file_id=2,
    # score=0.2). Only the low-scoring one (0.2 < 0.5 * 0.9) is judged insufficient alone
    # relative to the top score, triggering exactly one graph_neighbors call, for file_id=2.
    assert names.count("graph_neighbors") == 1
    graph_call = next(entry for entry in call_log if entry[0] == "graph_neighbors")
    assert graph_call[1] == (2, 2)  # (file_id, hops=DEFAULT_EXPANSION_HOPS)

    # get_file called once per final selected file_id (selected topics first, then newly-seen
    # expansion neighbors), after graph_neighbors.
    get_file_calls = [entry for entry in call_log if entry[0] == "get_file"]
    assert [entry[1][0] for entry in get_file_calls] == [1, 2, 3]

    # search_candidates -> graph_neighbors -> get_file (x3), strictly in that order.
    assert names == [
        "search_candidates",
        "graph_neighbors",
        "get_file",
        "get_file",
        "get_file",
    ]


def test_run_query_pipeline_response_shape() -> None:
    """Asserts `QueryPipelineResult`'s shape end-to-end: intent, selected_file_ids, synthesis."""

    def fake_search_candidates(query: str, max_results: int) -> list[TopicCandidate]:
        return _make_candidates()

    def fake_graph_neighbors(file_id: int, hops: int) -> list[GraphNeighbor]:
        return [GraphNeighbor(file_id=3, edge_type="ENTITY_COOCCUR", weight=1, hop=1)]

    def fake_get_file(file_id: int) -> str:
        return _FILE_CONTENT[file_id]

    llm_client = _FakeLLMClient([_INTENT_RESPONSE, _SYNTHESIS_RESPONSE])

    result = run_query_pipeline(
        "What's going on with invoice 4521?",
        ["earlier turn"],
        llm_client=llm_client,
        search_candidates=fake_search_candidates,
        graph_neighbors=fake_graph_neighbors,
        get_file=fake_get_file,
        k=2,
    )

    assert result.intent.refined_intent == "What is the status of the disputed invoice 4521?"
    assert result.intent.entities == ["invoice 4521"]
    assert result.intent.query_type == "factual_lookup"

    assert result.selected_file_ids == [1, 2, 3]

    assert result.synthesis.answer == (
        "Invoice 4521 was disputed for a duplicate charge [billing/InvoiceDisputes.md]."
    )
    assert result.synthesis.citations == ["billing/InvoiceDisputes.md"]
    assert result.synthesis.unknown_citations() == []

    # The prompt sent to the synthesis LLM call must embed both resolved files' headers.
    synthesis_prompt = llm_client.calls[1]["prompt"]
    assert "## File: billing/InvoiceDisputes.md" in synthesis_prompt
    # file_id=3 is reachable only via the graph_neighbors expansion (never present in
    # select_top_k's output), so no TopicCandidate.path exists for it -- see pipeline.py's
    # GetFileFn proto-shape fix disclosure for why this falls back to a placeholder path.
    assert "## File: (path unknown; file_id=3)" in synthesis_prompt


def test_run_query_pipeline_raises_on_empty_selection() -> None:
    """Asserts `PipelineError` is raised (not a vacuous synthesis call) when
    `search_candidates` returns nothing to select from."""

    def fake_search_candidates(query: str, max_results: int) -> list[TopicCandidate]:
        return []

    def fake_graph_neighbors(file_id: int, hops: int) -> list[GraphNeighbor]:
        raise AssertionError("graph_neighbors should not be called with no selected topics")

    def fake_get_file(file_id: int) -> str:
        raise AssertionError("get_file should not be called with no selected file_ids")

    llm_client = _FakeLLMClient([_INTENT_RESPONSE])

    with pytest.raises(PipelineError, match="no candidate files were selected"):
        run_query_pipeline(
            "an unanswerable query",
            [],
            llm_client=llm_client,
            search_candidates=fake_search_candidates,
            graph_neighbors=fake_graph_neighbors,
            get_file=fake_get_file,
            k=1,
        )

    # Only the intent-refinement LLM call should have happened, never the synthesis call.
    assert len(llm_client.calls) == 1
