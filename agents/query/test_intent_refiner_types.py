"""Tests for `query_type` classification-accuracy differentiation, per issue #22
subtask 4.3.2's own acceptance criteria and test spec.

Scope, relative to sibling `test_intent_refiner.py` (subtask 4.3.1's own file):
4.3.1's file already asserts `refine_intent()`'s *output shape* (does the parsed
result have the right fields/types?) using exactly one `factual_lookup` fixture and
one `broad_exploratory` fixture -- and its own docstring explicitly defers
"query_type classification accuracy across many fixture variants" to this file.
This file's job is narrower and different: prove that `refine_intent()` correctly
carries through -- i.e. *differentiates* -- both `query_type` categories the
topic-selector depends on, across multiple *additional*, non-overlapping
representative fixture queries per category (mirroring the acceptance criteria's
own example: "factual/lookup vs. broad/exploratory").

`intent_refiner.py` itself is not modified by this subtask; `refine_intent()` never
calls a real LLM provider -- classification is performed by the (real, external)
LLM and `refine_intent()` merely parses/validates its JSON response, so "testing
classification accuracy" here means: given a mocked `LLMClient.complete()` response
whose `query_type` field is set to the fixture's expected category, does
`refine_intent()` correctly surface that category on the returned
`IntentRefinerResult`, for each of several representative queries per category (not
just the two fixtures 4.3.1 already covers)? This is the same mocking boundary
4.3.1 established (`LLMClient` is a provider-agnostic DI seam per issue #18/#20; this
test suite never touches a concrete provider).
"""

from __future__ import annotations

import json

import pytest

from llm.client import LLMClient
from query.intent_refiner import IntentRefinerResult, QueryType, refine_intent

# ---------------------------------------------------------------------------
# Fixtures / fakes
# ---------------------------------------------------------------------------


class _FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string.

    Mirrors `test_intent_refiner.py`'s own `_FakeLLMClient` (and, further back,
    `ingestion/test_segment.py`'s precedent): a real ABC subclass, not
    `MagicMock(spec=LLMClient)`, so ABC compliance is exercised for real. Kept as
    a local copy rather than a shared import, since `agents/query/` has no shared
    test-helpers module and 4.3.1's own file follows the same self-contained,
    one-fake-per-test-file convention.
    """

    def __init__(self, response: str) -> None:
        self.response = response
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
        self.calls.append({"prompt": prompt, "model": model})
        return self.response


def _canned_response(query_type: str, refined_intent: str, entities: list[str]) -> str:
    """Build a well-formed LLM completion JSON string for `query_type`."""
    return json.dumps(
        {
            "refined_intent": refined_intent,
            "entities": entities,
            "query_type": query_type,
        }
    )


# Representative fixture queries, distinct from test_intent_refiner.py's two
# fixtures ("what's the total on invoice 4521?" / "tell me about our billing
# disputes"), spanning different domains within each query_type category.
_FACTUAL_LOOKUP_FIXTURES: list[tuple[str, str, list[str]]] = [
    (
        "What is the capital of France?",
        "What is the capital city of France?",
        ["France"],
    ),
    (
        "Who wrote the novel 1984?",
        "Who is the author of the novel '1984'?",
        ["1984"],
    ),
    (
        "What year did the Berlin Wall fall?",
        "In what year did the Berlin Wall fall?",
        ["Berlin Wall"],
    ),
]

_BROAD_EXPLORATORY_FIXTURES: list[tuple[str, str, list[str]]] = [
    (
        "Tell me everything about the history of the Roman Empire",
        "Provide a broad overview of the history of the Roman Empire.",
        ["Roman Empire"],
    ),
    (
        "Give me an overview of our company's product roadmap",
        "Summarize the company's overall product roadmap.",
        [],
    ),
    (
        "Summarize all the research on climate change adaptation strategies",
        "Summarize existing research on climate change adaptation strategies.",
        ["climate change adaptation"],
    ),
]

_ALL_FIXTURES: list[tuple[str, str, str, list[str]]] = [
    ("factual_lookup", query, refined, entities)
    for query, refined, entities in _FACTUAL_LOOKUP_FIXTURES
] + [
    ("broad_exploratory", query, refined, entities)
    for query, refined, entities in _BROAD_EXPLORATORY_FIXTURES
]


# ---------------------------------------------------------------------------
# Per-category classification tests
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "expected_type,query,refined_intent,entities",
    _ALL_FIXTURES,
    ids=[f"{qt}:{query}" for qt, query, _, _ in _ALL_FIXTURES],
)
def test_query_type_classification(
    expected_type: QueryType,
    query: str,
    refined_intent: str,
    entities: list[str],
) -> None:
    """Each representative fixture query classifies to its expected `query_type`
    when the mocked LLM response reports that category, across both taxonomy
    values (factual_lookup, broad_exploratory) per the acceptance criteria.
    """
    fake = _FakeLLMClient(_canned_response(expected_type, refined_intent, entities))

    result = refine_intent(query, [], fake)

    assert isinstance(result, IntentRefinerResult)
    assert result.query_type == expected_type


def test_factual_lookup_fixtures_all_classify_factual_lookup() -> None:
    """All factual/lookup fixtures land in the same category as each other."""
    for query, refined_intent, entities in _FACTUAL_LOOKUP_FIXTURES:
        fake = _FakeLLMClient(_canned_response("factual_lookup", refined_intent, entities))
        result = refine_intent(query, [], fake)
        assert result.query_type == "factual_lookup"


def test_broad_exploratory_fixtures_all_classify_broad_exploratory() -> None:
    """All broad/exploratory fixtures land in the same category as each other."""
    for query, refined_intent, entities in _BROAD_EXPLORATORY_FIXTURES:
        fake = _FakeLLMClient(_canned_response("broad_exploratory", refined_intent, entities))
        result = refine_intent(query, [], fake)
        assert result.query_type == "broad_exploratory"


def test_query_type_differentiates_categories() -> None:
    """Direct differentiation assertion: a factual/lookup fixture and a
    broad/exploratory fixture yield *different* `query_type` values from each
    other, per the acceptance criteria's own wording ("correctly differentiates
    ... query_type categories"), not merely two independently-correct values.
    """
    factual_query, factual_refined, factual_entities = _FACTUAL_LOOKUP_FIXTURES[0]
    broad_query, broad_refined, broad_entities = _BROAD_EXPLORATORY_FIXTURES[0]

    factual_fake = _FakeLLMClient(
        _canned_response("factual_lookup", factual_refined, factual_entities)
    )
    broad_fake = _FakeLLMClient(
        _canned_response("broad_exploratory", broad_refined, broad_entities)
    )

    factual_result = refine_intent(factual_query, [], factual_fake)
    broad_result = refine_intent(broad_query, [], broad_fake)

    assert factual_result.query_type == "factual_lookup"
    assert broad_result.query_type == "broad_exploratory"
    assert factual_result.query_type != broad_result.query_type
