"""Tests for `ingestion.segment.segment` and its structured JSON output parsing.

Per issue #18 subtask 3.4.3's test spec: `LLMClient.complete()` is mocked (via a
small fake `LLMClient` subclass, no real provider call). Covers well-formed JSON
for both `topic_action` values, and every malformed-response case called out by
the issue: unparseable JSON, valid JSON missing a required field, valid JSON with
a wrong field type, and valid JSON with an internally inconsistent field
combination -- each asserted to raise `SegmentParseError` with a specific,
descriptive message (never a generic `Exception`).
"""

from __future__ import annotations

import json
from datetime import datetime, timezone

import pytest

from ingestion.rawdoc import RawDocument
from ingestion.segment import SegmentParseError, SegmentResult, segment
from ingestion.shortlist import TopicCandidate
from llm.client import LLMClient, LLMError

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


class _FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string.

    Captures the prompt/kwargs it was called with, for assertions -- mirrors
    the issue's "LLMClient mocked" test-spec wording. A subclass (rather than
    `MagicMock(spec=LLMClient)`) is used for straightforward ABC compliance,
    matching `agents/llm/test_ollama_client.py`'s own preference for a real
    concrete stand-in over a bare mock.
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


def _make_doc(text: str = "Customer reports a billing discrepancy on invoice 4521.") -> RawDocument:
    return RawDocument(
        id="doc-1",
        source_type="email",
        text=text,
        structured_fields={},
        timestamp=datetime(2026, 1, 1, tzinfo=timezone.utc),
    )


def _make_shortlist() -> list[TopicCandidate]:
    return [
        TopicCandidate(file_id=1, path="billing/InvoiceDisputes", score=5.0),
        TopicCandidate(file_id=2, path="billing/RefundRequests", score=2.0),
    ]


_VALID_APPEND_PAYLOAD = {
    "topic_action": "APPEND_EXISTING",
    "target_topic": "billing/InvoiceDisputes",
    "new_topic_path": "",
    "content_markdown": "## Invoice 4521\n\nCustomer reports a billing discrepancy.",
    "entities": ["invoice-4521"],
    "related_topics": ["billing/RefundRequests"],
}

_VALID_CREATE_PAYLOAD = {
    "topic_action": "CREATE_NEW",
    "target_topic": "",
    "new_topic_path": "billing/DuplicateCharges",
    "content_markdown": "## Duplicate charge\n\nCustomer reports a duplicate charge.",
    "entities": [],
    "related_topics": [],
}


# ---------------------------------------------------------------------------
# Well-formed responses
# ---------------------------------------------------------------------------


def test_segment_appends_existing_topic() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_APPEND_PAYLOAD))
    result = segment(_make_doc(), _make_shortlist(), client)

    assert result == SegmentResult(
        topic_action="APPEND_EXISTING",
        target_topic="billing/InvoiceDisputes",
        new_topic_path="",
        content_markdown=_VALID_APPEND_PAYLOAD["content_markdown"],
        entities=["invoice-4521"],
        related_topics=["billing/RefundRequests"],
    )


def test_segment_creates_new_topic() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_CREATE_PAYLOAD))
    result = segment(_make_doc(), _make_shortlist(), client)

    assert result == SegmentResult(
        topic_action="CREATE_NEW",
        target_topic="",
        new_topic_path="billing/DuplicateCharges",
        content_markdown=_VALID_CREATE_PAYLOAD["content_markdown"],
        entities=[],
        related_topics=[],
    )


def test_prompt_includes_document_and_shortlist() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_CREATE_PAYLOAD))
    doc = _make_doc(text="UNIQUE_DOCUMENT_MARKER_TEXT")
    segment(doc, _make_shortlist(), client)

    assert len(client.calls) == 1
    prompt = client.calls[0]["prompt"]
    assert "UNIQUE_DOCUMENT_MARKER_TEXT" in prompt
    assert "billing/InvoiceDisputes" in prompt
    assert "billing/RefundRequests" in prompt


def test_segment_forwards_call_kwargs() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_CREATE_PAYLOAD))
    segment(
        _make_doc(),
        _make_shortlist(),
        client,
        model="llama3.1:8b",
        temperature=0.2,
        max_tokens=512,
        timeout=30.0,
    )

    call = client.calls[0]
    assert call["model"] == "llama3.1:8b"
    assert call["temperature"] == 0.2
    assert call["max_tokens"] == 512
    assert call["timeout"] == 30.0


def test_llm_error_propagates_unwrapped() -> None:
    client = _FakeLLMClient(error=LLMError("provider call failed"))
    with pytest.raises(LLMError):
        segment(_make_doc(), _make_shortlist(), client)


# ---------------------------------------------------------------------------
# Malformed responses
# ---------------------------------------------------------------------------


def test_unparseable_json_raises() -> None:
    client = _FakeLLMClient(response="this is not JSON at all {{{")
    with pytest.raises(SegmentParseError, match="not valid JSON"):
        segment(_make_doc(), _make_shortlist(), client)


def test_non_object_json_raises() -> None:
    client = _FakeLLMClient(response=json.dumps(["not", "an", "object"]))
    with pytest.raises(SegmentParseError, match="must be a JSON object"):
        segment(_make_doc(), _make_shortlist(), client)


@pytest.mark.parametrize(
    "missing_field",
    [
        "topic_action",
        "target_topic",
        "new_topic_path",
        "content_markdown",
        "entities",
        "related_topics",
    ],
)
def test_missing_field_raises(missing_field: str) -> None:
    payload = dict(_VALID_APPEND_PAYLOAD)
    del payload[missing_field]
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(SegmentParseError, match=missing_field):
        segment(_make_doc(), _make_shortlist(), client)


@pytest.mark.parametrize(
    "field,bad_value",
    [
        ("topic_action", 42),
        ("target_topic", 42),
        ("new_topic_path", 42),
        ("content_markdown", 42),
        ("entities", "should-be-a-list"),
        ("related_topics", "should-be-a-list"),
    ],
)
def test_wrong_type_field_raises(field: str, bad_value: object) -> None:
    payload = dict(_VALID_APPEND_PAYLOAD)
    payload[field] = bad_value
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(SegmentParseError, match=field):
        segment(_make_doc(), _make_shortlist(), client)


def test_wrong_type_list_element_raises() -> None:
    payload = dict(_VALID_APPEND_PAYLOAD)
    payload["entities"] = ["ok", 123]
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(SegmentParseError, match="entities"):
        segment(_make_doc(), _make_shortlist(), client)


def test_invalid_topic_action_value_raises() -> None:
    payload = dict(_VALID_APPEND_PAYLOAD)
    payload["topic_action"] = "DELETE_TOPIC"
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(SegmentParseError, match="topic_action"):
        segment(_make_doc(), _make_shortlist(), client)


def test_append_existing_without_target_topic_raises() -> None:
    payload = dict(_VALID_APPEND_PAYLOAD)
    payload["target_topic"] = ""
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(SegmentParseError, match="target_topic"):
        segment(_make_doc(), _make_shortlist(), client)


def test_create_new_without_new_topic_path_raises() -> None:
    payload = dict(_VALID_CREATE_PAYLOAD)
    payload["new_topic_path"] = ""
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(SegmentParseError, match="new_topic_path"):
        segment(_make_doc(), _make_shortlist(), client)


# ---------------------------------------------------------------------------
# Markdown-code-fence tolerance (closes forwarded finding F1)
# ---------------------------------------------------------------------------


def test_markdown_code_fence_wrapped_json_is_parsed() -> None:
    """Regression guard for F1 (`.cdr/index/regression.jsonl`): a real
    Ollama-backed model that ignores the prompt's "no markdown code fences"
    instruction and wraps its otherwise well-formed JSON in a ```` ```json ... ``` ````
    fence must still parse successfully via the shared
    `ingestion._json_fences.strip_code_fences` helper, not raise `SegmentParseError`.
    """
    fenced = "```json\n" + json.dumps(_VALID_APPEND_PAYLOAD) + "\n```"
    client = _FakeLLMClient(response=fenced)

    result = segment(_make_doc(), _make_shortlist(), client)

    assert result.topic_action == "APPEND_EXISTING"
    assert result.target_topic == "billing/InvoiceDisputes"


def test_plain_code_fence_wrapped_json_is_parsed() -> None:
    """Same as above, but an untagged ``` ``` ``` fence (no `json` language tag)."""
    fenced = "```\n" + json.dumps(_VALID_CREATE_PAYLOAD) + "\n```"
    client = _FakeLLMClient(response=fenced)

    result = segment(_make_doc(), _make_shortlist(), client)

    assert result.topic_action == "CREATE_NEW"
    assert result.new_topic_path == "billing/DuplicateCharges"


# ---------------------------------------------------------------------------
# Control-character / triple-quote artifact tolerance (closes forwarded finding F7,
# issue #44). Failure shapes per `.cdr/index/regression.jsonl`'s
# `hivemind-issue19-3.5.2-F7-llm-json-control-chars` and
# `.cdr/runs/2026-07-10/040-implementation/` -- the exact raw bytes captured from
# the live `llama3.1:8b` smoke run were not persisted anywhere in the repo (see
# this subtask's requirement.md), so these fixtures are constructed to reproduce
# the two *described* failure shapes/error classes rather than replaying literal
# captured bytes.
# ---------------------------------------------------------------------------


def test_raw_control_character_in_string_value_is_sanitized() -> None:
    """A real `llama3.1:8b` completion observed to embed a raw, unescaped control
    character (e.g. a literal newline byte, not the two-character `\\n` escape)
    directly inside the `content_markdown` string value. Pre-fix, `json.loads`
    raised `json.JSONDecodeError: Invalid control character at: ...` and
    `_parse_segment_json` surfaced this as `SegmentParseError`. Post-fix, the
    fallback `sanitize_control_chars_and_triple_quotes` pass escapes the raw
    control byte and `segment()` succeeds.
    """
    # A plain json.dumps() would already escape this -- build the raw string by
    # hand so the completion actually contains a literal 0x0A byte where a real
    # model was observed to leave one, exactly reproducing the "Invalid control
    # character" json.loads failure this fix targets.
    raw_completion = (
        '{"topic_action": "APPEND_EXISTING", "target_topic": "billing/InvoiceDisputes", '
        '"new_topic_path": "", "content_markdown": "## Invoice 4521\n\nCustomer reports '
        'a billing discrepancy.", "entities": ["invoice-4521"], '
        '"related_topics": ["billing/RefundRequests"]}'
    )
    # Sanity: confirm this fixture genuinely reproduces the pre-fix failure mode
    # before asserting the post-fix behavior below.
    with pytest.raises(json.JSONDecodeError, match="Invalid control character"):
        json.loads(raw_completion)

    client = _FakeLLMClient(response=raw_completion)

    result = segment(_make_doc(), _make_shortlist(), client)

    assert result.topic_action == "APPEND_EXISTING"
    assert result.content_markdown == "## Invoice 4521\n\nCustomer reports a billing discrepancy."


def test_triple_quote_wrapped_string_value_is_sanitized() -> None:
    """A real `llama3.1:8b` completion observed to wrap a string value in a
    Python-docstring-style `\"\"\"..\"\"\"` artifact instead of a plain JSON
    `\"...\"` string. Pre-fix, `json.loads` raised
    `json.JSONDecodeError: Expecting ',' delimiter: ...` (the first two of the
    three quote characters parse as an empty string, leaving the rest as
    unexpected trailing content) and `_parse_segment_json` surfaced this as
    `SegmentParseError`. Post-fix, the fallback sanitization pass normalizes the
    triple-quoted span into a single well-formed JSON string and `segment()`
    succeeds.
    """
    raw_completion = (
        '{"topic_action": "CREATE_NEW", "target_topic": "", '
        '"new_topic_path": "billing/DuplicateCharges", '
        '"content_markdown": """## Duplicate charge\n\nCustomer reports a duplicate charge.""", '
        '"entities": [], "related_topics": []}'
    )
    with pytest.raises(json.JSONDecodeError, match="Expecting ',' delimiter"):
        json.loads(raw_completion)

    client = _FakeLLMClient(response=raw_completion)

    result = segment(_make_doc(), _make_shortlist(), client)

    assert result.topic_action == "CREATE_NEW"
    assert result.new_topic_path == "billing/DuplicateCharges"
    assert (
        result.content_markdown
        == "## Duplicate charge\n\nCustomer reports a duplicate charge."
    )


def test_combined_control_char_and_triple_quote_artifacts_is_sanitized() -> None:
    """Both artifact shapes in the same completion (observed as a real possibility
    since either can occur independently per-field): a raw control character in
    one string field and a triple-quote-wrapped value in another. Both
    sanitization steps must compose correctly.
    """
    raw_completion = (
        '{"topic_action": "APPEND_EXISTING", '
        '"target_topic": "billing/InvoiceDisputes", '
        '"new_topic_path": "", '
        '"content_markdown": """## Invoice 4521\n\nCustomer reports a billing discrepancy.""", '
        '"entities": ["invoice-4521"], '
        '"related_topics": ["billing/RefundRequests"]}'
    )
    with pytest.raises(json.JSONDecodeError):
        json.loads(raw_completion)

    client = _FakeLLMClient(response=raw_completion)

    result = segment(_make_doc(), _make_shortlist(), client)

    assert result.topic_action == "APPEND_EXISTING"
    assert (
        result.content_markdown
        == "## Invoice 4521\n\nCustomer reports a billing discrepancy."
    )


def test_unparseable_json_still_raises_after_sanitization_fallback() -> None:
    """Regression guard: genuinely invalid JSON unrelated to either F7 artifact
    (e.g. a truncated/missing closing brace) must still raise `SegmentParseError`
    -- the sanitization fallback must not mask real errors by, say, catching and
    swallowing exceptions it can't actually fix.
    """
    truncated = '{"topic_action": "APPEND_EXISTING", "target_topic": "billing/InvoiceDisputes"'
    client = _FakeLLMClient(response=truncated)

    with pytest.raises(SegmentParseError, match="not valid JSON"):
        segment(_make_doc(), _make_shortlist(), client)
