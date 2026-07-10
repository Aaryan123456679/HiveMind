"""Tests for `query.intent_refiner.refine_intent` and its structured JSON output parsing.

Per issue #22 subtask 4.3.1's test spec: `LLMClient.complete()` is mocked (via a small fake
`LLMClient` subclass, no real provider call). Covers well-formed JSON for representative
fixture queries (factual/lookup, broad/exploratory, and one with non-empty history), plus
every malformed-response case mirrored from `ingestion.test_segment`'s own precedent:
unparseable JSON, valid JSON missing a required field, valid JSON with a wrong field type,
and valid JSON with an invalid `query_type` enum value -- each asserted to raise
`IntentRefinerParseError` with a specific, descriptive message (never a generic
`Exception`). Also covers markdown-code-fence tolerance and `LLMError` propagation.

Note: `query_type` classification *accuracy* across many fixture variants is subtask 4.3.2's
own test spec (`test_intent_refiner_types.py`, a separate future dispatch on the same issue),
not this file's job -- this file only asserts the *output shape* per 4.3.1's own acceptance
criteria.
"""

from __future__ import annotations

import json

import pytest

from llm.client import LLMClient, LLMError
from query.intent_refiner import (
    IntentRefinerParseError,
    IntentRefinerResult,
    refine_intent,
)

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


class _FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string.

    Mirrors `ingestion.test_segment._FakeLLMClient`: captures the prompt/kwargs it was
    called with, for assertions, and is a real ABC subclass (not `MagicMock(spec=LLMClient)`)
    for straightforward ABC compliance.
    """

    def __init__(self, response: str | None = None, error: Exception | None = None) -> None:
        self.response = response
        self.error = error
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
        if self.error is not None:
            raise self.error
        assert self.response is not None
        return self.response


def _well_formed_json(
    refined_intent: str = "Find the invoice total for invoice 4521.",
    entities: list[str] | None = None,
    query_type: str = "factual_lookup",
) -> str:
    return json.dumps(
        {
            "refined_intent": refined_intent,
            "entities": entities if entities is not None else ["invoice 4521"],
            "query_type": query_type,
        }
    )


# ---------------------------------------------------------------------------
# Well-formed / output-shape tests (representative fixture queries)
# ---------------------------------------------------------------------------


def test_refine_intent_factual_lookup() -> None:
    fake = _FakeLLMClient(
        response=_well_formed_json(
            refined_intent="What is the total amount on invoice 4521?",
            entities=["invoice 4521"],
            query_type="factual_lookup",
        )
    )

    result = refine_intent("what's the total on invoice 4521?", [], fake)

    assert isinstance(result, IntentRefinerResult)
    assert result.refined_intent == "What is the total amount on invoice 4521?"
    assert result.entities == ["invoice 4521"]
    assert result.query_type == "factual_lookup"


def test_refine_intent_broad_exploratory() -> None:
    fake = _FakeLLMClient(
        response=_well_formed_json(
            refined_intent="Summarize all known billing disputes across customers.",
            entities=[],
            query_type="broad_exploratory",
        )
    )

    result = refine_intent("tell me about our billing disputes", [], fake)

    assert isinstance(result, IntentRefinerResult)
    assert result.entities == []
    assert result.query_type == "broad_exploratory"


def test_refine_intent_with_history_included_in_prompt() -> None:
    fake = _FakeLLMClient(
        response=_well_formed_json(
            refined_intent="What is the status of the invoice 4521 dispute we discussed?",
            entities=["invoice 4521"],
            query_type="factual_lookup",
        )
    )
    history = ["Customer reported a billing discrepancy on invoice 4521."]

    result = refine_intent("what's the status of that?", history, fake)

    assert result.refined_intent == "What is the status of the invoice 4521 dispute we discussed?"
    assert len(fake.calls) == 1
    assert "invoice 4521" in fake.calls[0]["prompt"]
    assert "what's the status of that?" in fake.calls[0]["prompt"]


def test_refine_intent_forwards_call_kwargs() -> None:
    fake = _FakeLLMClient(response=_well_formed_json())

    refine_intent(
        "what's the total on invoice 4521?",
        [],
        fake,
        model="some-model",
        temperature=0.2,
        max_tokens=256,
        timeout=5.0,
    )

    assert len(fake.calls) == 1
    call = fake.calls[0]
    assert call["model"] == "some-model"
    assert call["temperature"] == 0.2
    assert call["max_tokens"] == 256
    assert call["timeout"] == 5.0


def test_refine_intent_strips_code_fence() -> None:
    fenced = "```json\n" + _well_formed_json() + "\n```"
    fake = _FakeLLMClient(response=fenced)

    result = refine_intent("what's the total on invoice 4521?", [], fake)

    assert isinstance(result, IntentRefinerResult)
    assert result.query_type == "factual_lookup"


# ---------------------------------------------------------------------------
# Malformed-output tests
# ---------------------------------------------------------------------------


def test_refine_intent_invalid_json_raises() -> None:
    fake = _FakeLLMClient(response="not json at all {")

    with pytest.raises(IntentRefinerParseError, match="not valid JSON"):
        refine_intent("query", [], fake)


def test_refine_intent_missing_field_raises() -> None:
    payload = json.loads(_well_formed_json())
    del payload["query_type"]
    fake = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(IntentRefinerParseError, match="query_type"):
        refine_intent("query", [], fake)


def test_refine_intent_wrong_type_raises() -> None:
    payload = json.loads(_well_formed_json())
    payload["entities"] = "invoice 4521"  # should be a list
    fake = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(IntentRefinerParseError, match="entities"):
        refine_intent("query", [], fake)


def test_refine_intent_invalid_query_type_raises() -> None:
    payload = json.loads(_well_formed_json())
    payload["query_type"] = "something_else"
    fake = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(IntentRefinerParseError, match="query_type"):
        refine_intent("query", [], fake)


def test_refine_intent_llm_error_propagates() -> None:
    fake = _FakeLLMClient(error=LLMError("provider timed out"))

    with pytest.raises(LLMError, match="provider timed out"):
        refine_intent("query", [], fake)
