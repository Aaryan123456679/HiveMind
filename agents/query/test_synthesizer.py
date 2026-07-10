"""Tests for `query.synthesizer.synthesize_answer` and its structured JSON output parsing.

Per issue #24 subtask 4.5.1's test spec: `LLMClient.complete()` is mocked (via a small fake
`LLMClient` subclass, mirroring `test_intent_refiner.py`'s own `_FakeLLMClient` precedent --
no real provider call). Asserts the prompt includes the "## File: <path>" file-path headers
from the input `selected_markdown` verbatim, and that output parsing extracts the `answer`
and `citations` fields correctly from a fixture citation-containing LLM response.

Also covers `SynthesizerResult.unknown_citations()` -- the building block subtask 4.5.2
will build its own dedicated validation test/logic on top of (this file does not implement
4.5.2's own rejection behavior or test file) -- and every malformed-response case mirrored
from `test_intent_refiner.py`'s own precedent: unparseable JSON, valid JSON missing a
required field, and valid JSON with a wrong field type, each asserted to raise
`SynthesizerParseError` with a specific, descriptive message. Also covers markdown-code-
fence tolerance and `LLMError` propagation.
"""

from __future__ import annotations

import json

import pytest

from llm.client import LLMClient, LLMError
from query.synthesizer import (
    SynthesizerParseError,
    SynthesizerResult,
    synthesize_answer,
)

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


class _FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string.

    Mirrors `test_intent_refiner._FakeLLMClient`: captures the prompt/kwargs it was called
    with, for assertions, and is a real ABC subclass (not `MagicMock(spec=LLMClient)`) for
    straightforward ABC compliance.
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


#: Concatenated selected markdown with two "## File: <path>" headers, per this module's
#: documented header format.
_SELECTED_MARKDOWN = """## File: billing/InvoiceDisputes.md
Invoice 4521 was disputed by the customer on 2026-01-03 for a duplicate charge.

## File: billing/PaymentDelays.md
Payment for invoice 4521 was delayed pending resolution of the dispute.
"""


def _well_formed_json(
    answer: str = (
        "Invoice 4521 was disputed for a duplicate charge "
        "[billing/InvoiceDisputes.md], and payment was delayed pending resolution "
        "[billing/PaymentDelays.md]."
    ),
    citations: list[str] | None = None,
) -> str:
    return json.dumps(
        {
            "answer": answer,
            "citations": (
                citations
                if citations is not None
                else ["billing/InvoiceDisputes.md", "billing/PaymentDelays.md"]
            ),
        }
    )


def _synthesize(response: str | None = None, error: Exception | None = None, **kwargs):
    fake = _FakeLLMClient(response=response, error=error)
    result = synthesize_answer(
        "What happened with invoice 4521?",
        "factual_lookup",
        ["invoice 4521"],
        _SELECTED_MARKDOWN,
        fake,
        **kwargs,
    )
    return result, fake


# ---------------------------------------------------------------------------
# Core acceptance criteria: prompt assembly + citation extraction
# ---------------------------------------------------------------------------


def test_prompt_includes_file_path_headers() -> None:
    fake = _FakeLLMClient(response=_well_formed_json())

    synthesize_answer(
        "What happened with invoice 4521?",
        "factual_lookup",
        ["invoice 4521"],
        _SELECTED_MARKDOWN,
        fake,
    )

    assert len(fake.calls) == 1
    prompt = fake.calls[0]["prompt"]
    assert "## File: billing/InvoiceDisputes.md" in prompt
    assert "## File: billing/PaymentDelays.md" in prompt
    assert "What happened with invoice 4521?" in prompt
    assert "factual_lookup" in prompt
    assert "invoice 4521" in prompt


def test_synthesize_answer_extracts_citations_and_answer() -> None:
    result, _ = _synthesize(response=_well_formed_json())

    assert isinstance(result, SynthesizerResult)
    assert "[billing/InvoiceDisputes.md]" in result.answer
    assert "[billing/PaymentDelays.md]" in result.answer
    assert result.citations == ["billing/InvoiceDisputes.md", "billing/PaymentDelays.md"]
    assert result.provided_paths == [
        "billing/InvoiceDisputes.md",
        "billing/PaymentDelays.md",
    ]


def test_synthesize_answer_forwards_call_kwargs() -> None:
    _, fake = _synthesize(
        response=_well_formed_json(),
        model="some-model",
        temperature=0.3,
        max_tokens=512,
        timeout=10.0,
    )

    assert len(fake.calls) == 1
    call = fake.calls[0]
    assert call["model"] == "some-model"
    assert call["temperature"] == 0.3
    assert call["max_tokens"] == 512
    assert call["timeout"] == 10.0


def test_synthesize_answer_strips_code_fence() -> None:
    fenced = "```json\n" + _well_formed_json() + "\n```"

    result, _ = _synthesize(response=fenced)

    assert result.citations == ["billing/InvoiceDisputes.md", "billing/PaymentDelays.md"]


def test_synthesize_answer_no_provided_headers() -> None:
    """With no "## File:" headers in the input, `provided_paths` is empty regardless of
    what the LLM claims to have cited."""
    fake = _FakeLLMClient(response=_well_formed_json())

    result = synthesize_answer(
        "What happened with invoice 4521?",
        "factual_lookup",
        ["invoice 4521"],
        "No headers here, just prose.",
        fake,
    )

    assert result.provided_paths == []
    assert result.citations == ["billing/InvoiceDisputes.md", "billing/PaymentDelays.md"]


# ---------------------------------------------------------------------------
# unknown_citations() -- building block for subtask 4.5.2
# ---------------------------------------------------------------------------


def test_unknown_citations_empty_when_all_citations_provided() -> None:
    result, _ = _synthesize(response=_well_formed_json())

    assert result.unknown_citations() == []


def test_unknown_citations_flags_path_not_in_provided_set() -> None:
    result, _ = _synthesize(
        response=_well_formed_json(
            answer=(
                "Invoice 4521 was disputed [billing/InvoiceDisputes.md], per an "
                "internal memo [legal/InternalMemo.md]."
            ),
            citations=["billing/InvoiceDisputes.md", "legal/InternalMemo.md"],
        )
    )

    assert result.unknown_citations() == ["legal/InternalMemo.md"]
    # The valid citation is untouched -- only the hallucinated one is flagged.
    assert "billing/InvoiceDisputes.md" not in result.unknown_citations()


def test_unknown_citations_deduplicated_and_order_preserved() -> None:
    result, _ = _synthesize(
        response=_well_formed_json(
            answer="See [legal/Ghost.md] and again [legal/Ghost.md] and [hr/Ghost2.md].",
            citations=["legal/Ghost.md", "legal/Ghost.md", "hr/Ghost2.md"],
        )
    )

    assert result.unknown_citations() == ["legal/Ghost.md", "hr/Ghost2.md"]


# ---------------------------------------------------------------------------
# Malformed-output tests
# ---------------------------------------------------------------------------


def test_synthesize_answer_invalid_json_raises() -> None:
    with pytest.raises(SynthesizerParseError, match="not valid JSON"):
        _synthesize(response="not json at all {")


def test_synthesize_answer_missing_field_raises() -> None:
    payload = json.loads(_well_formed_json())
    del payload["citations"]
    with pytest.raises(SynthesizerParseError, match="citations"):
        _synthesize(response=json.dumps(payload))


def test_synthesize_answer_wrong_type_raises() -> None:
    payload = json.loads(_well_formed_json())
    payload["citations"] = "billing/InvoiceDisputes.md"  # should be a list
    with pytest.raises(SynthesizerParseError, match="citations"):
        _synthesize(response=json.dumps(payload))


def test_synthesize_answer_wrong_answer_type_raises() -> None:
    payload = json.loads(_well_formed_json())
    payload["answer"] = ["not", "a", "string"]
    with pytest.raises(SynthesizerParseError, match="answer"):
        _synthesize(response=json.dumps(payload))


def test_synthesize_answer_non_object_json_raises() -> None:
    with pytest.raises(SynthesizerParseError, match="JSON object"):
        _synthesize(response=json.dumps(["not", "an", "object"]))


def test_synthesize_answer_llm_error_propagates() -> None:
    with pytest.raises(LLMError, match="provider timed out"):
        _synthesize(error=LLMError("provider timed out"))
