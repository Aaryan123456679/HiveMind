"""Dedicated citation-format validation test for `query.synthesizer`.

Per issue #24 subtask 4.5.2's acceptance criteria: any citation in the synthesized answer
must reference a file path that was actually present in the selected-file input set;
citations to unknown paths must be detected/flagged.

`SynthesizerResult.unknown_citations()` (built in 4.5.1 explicitly as "a defensible building
block for subtask 4.5.2's dedicated citation-format validation") already implements this
detection correctly and is partially exercised by `test_synthesizer.py`'s own
`unknown_citations()`-focused tests. Per this run's `requirement.md`, reading `synthesizer.py`
end to end found no gap in `synthesize_answer()`/`unknown_citations()` that would justify a
production-code change: the acceptance criteria ("flagged/rejected") and test spec
("detected/flagged") are disjunctive, and a callable, correctly-behaving detection method
that a caller can inspect on the returned `SynthesizerResult` satisfies "flagged" without
forcing 4.5.1's already-settled, disclosed non-raising design to change. This subtask is
therefore test-file-only (matching its own "Impacted modules" list) -- no synthesizer.py
change was made.

This file's job, distinct from `test_synthesizer.py`'s existing `unknown_citations()`
coverage, is to prove the *specific* end-to-end scenario 4.5.2's test spec describes: a
fixture LLM response containing exactly one valid citation and one hallucinated citation
(embedded in realistic answer prose), exercised through `synthesize_answer()` with a mocked
`LLMClient` (mirroring `test_synthesizer.py`'s own `_FakeLLMClient` pattern) -- not
unit-testing `unknown_citations()` in isolation against a hand-built `SynthesizerResult`.
"""

from __future__ import annotations

import json

from llm.client import LLMClient
from query.synthesizer import SynthesizerResult, synthesize_answer

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


class _FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string.

    Mirrors `test_synthesizer._FakeLLMClient` / `test_intent_refiner._FakeLLMClient`: a real
    ABC subclass (not `MagicMock(spec=LLMClient)`), returning a fixed response regardless of
    the prompt it is called with.
    """

    def __init__(self, response: str) -> None:
        self.response = response

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        return self.response


#: The selected-file input set: exactly two "## File: <path>" headers, per synthesizer.py's
#: documented header format. Only these two paths are "actually present" per the acceptance
#: criteria's "selected-file input set".
_SELECTED_MARKDOWN = """## File: billing/InvoiceDisputes.md
Invoice 4521 was disputed by the customer on 2026-01-03 for a duplicate charge.

## File: billing/PaymentDelays.md
Payment for invoice 4521 was delayed pending resolution of the dispute.
"""

_VALID_PATH = "billing/InvoiceDisputes.md"
_HALLUCINATED_PATH = "legal/InternalMemo.md"


def _fixture_response(*, hallucinated_first: bool) -> str:
    """Build a fixture LLM JSON response containing exactly one valid citation
    (`_VALID_PATH`, present in `_SELECTED_MARKDOWN`'s headers) and one hallucinated citation
    (`_HALLUCINATED_PATH`, absent from `_SELECTED_MARKDOWN` entirely), per 4.5.2's test spec.

    `hallucinated_first` controls whether the hallucinated citation appears before or after
    the valid one in both `answer`'s prose and the `citations` array, to guard against an
    accidental positional assumption in detection.
    """
    if hallucinated_first:
        answer = (
            f"Per an internal memo [{_HALLUCINATED_PATH}], invoice 4521 was disputed for a "
            f"duplicate charge [{_VALID_PATH}]."
        )
        citations = [_HALLUCINATED_PATH, _VALID_PATH]
    else:
        answer = (
            f"Invoice 4521 was disputed for a duplicate charge [{_VALID_PATH}], per an "
            f"internal memo [{_HALLUCINATED_PATH}]."
        )
        citations = [_VALID_PATH, _HALLUCINATED_PATH]
    return json.dumps({"answer": answer, "citations": citations})


def _synthesize(*, hallucinated_first: bool = False) -> SynthesizerResult:
    fake = _FakeLLMClient(response=_fixture_response(hallucinated_first=hallucinated_first))
    return synthesize_answer(
        "What happened with invoice 4521?",
        "factual_lookup",
        ["invoice 4521"],
        _SELECTED_MARKDOWN,
        fake,
    )


# ---------------------------------------------------------------------------
# 4.5.2 acceptance criteria: one valid + one hallucinated citation
# ---------------------------------------------------------------------------


def test_valid_citation_not_flagged_as_unknown() -> None:
    """A citation matching a real "## File: <path>" header in the selected-file input set
    must NOT be flagged as unknown."""
    result = _synthesize()

    assert _VALID_PATH not in result.unknown_citations()


def test_hallucinated_citation_is_flagged_as_unknown() -> None:
    """A citation to a path absent from the selected-file input set must be detected/flagged
    via `unknown_citations()` -- the core 4.5.2 acceptance criterion."""
    result = _synthesize()

    assert _HALLUCINATED_PATH in result.unknown_citations()


def test_mixed_valid_and_hallucinated_citations_only_hallucinated_flagged() -> None:
    """Given a fixture LLM response with exactly one valid and one hallucinated citation
    (the exact scenario named by 4.5.2's test spec): only the hallucinated one is flagged,
    and the raw `citations` list is untouched (flagging is a distinct, additive signal, not a
    silent filter/mutation of the reported citations)."""
    result = _synthesize()

    assert result.unknown_citations() == [_HALLUCINATED_PATH]
    assert result.citations == [_VALID_PATH, _HALLUCINATED_PATH]
    assert result.provided_paths == [_VALID_PATH, "billing/PaymentDelays.md"]


def test_hallucinated_citation_flagged_regardless_of_position() -> None:
    """Detection does not depend on whether the hallucinated citation appears before or
    after the valid one in `answer`/`citations`."""
    result = _synthesize(hallucinated_first=True)

    assert result.unknown_citations() == [_HALLUCINATED_PATH]
    assert _VALID_PATH not in result.unknown_citations()
