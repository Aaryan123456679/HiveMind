"""Intent refinement: LLM call + structured JSON output parsing/validation.

Per issue #22 subtask 4.3.1 and `docs/LLD/query-agent.md`'s "`intent_refiner.py`" section:
given a raw user query and a short conversation history, build an intent-refinement prompt,
call it through an `LLMClient` (issue #18/#20's provider-agnostic DI interface,
`agents/llm/client.py`), and parse the model's raw completion string into a validated
`IntentRefinerResult` matching the LLD's flat JSON shape:

```
{
  refined_intent: str,
  entities: [],
  query_type: "factual_lookup" | "broad_exploratory"
}
```

`query_type` taxonomy -- disclosed choice
------------------------------------------
`docs/LLD/query-agent.md` names the `query_type` field but does not specify its value set;
it is authored here. This module's sibling subtask on the same issue, 4.3.2 ("unit tests
covering query_type variants"), states its own acceptance criteria in terms of "at least the
query_type categories the topic-selector depends on (e.g. factual/lookup vs.
broad/exploratory)". This module adopts exactly those two categories --
`"factual_lookup"` and `"broad_exploratory"` -- as the minimal taxonomy: it is the only one
named anywhere in the issue or LLD, and picking anything richer now would risk diverging
from what 4.3.2 actually needs to classify against. `topic_selector.py` (not built yet, LLD
scaffold only) is expected to key its own top-k/graph-expansion behavior off this field.

`history` shape -- disclosed choice
------------------------------------
The issue and LLD both say "short history" with no richer structured-turn schema (no
speaker/role, no timestamps) specified anywhere. This module therefore accepts
`history: Sequence[str]` -- a flat list of prior raw utterance strings, most-recent-last --
mirroring `ingestion.segment.SegmentResult.entities`' own precedent of treating an
under-specified list field as `list[str]` rather than inventing a richer per-item shape the
issue never asked for.

Prompt-then-parse-JSON pattern
--------------------------------
This module mirrors `agents/ingestion/segment.py`'s established shape (itself following
`agents/ingestion/propose_split.py`): build a prompt, call `LLMClient.complete()`, strip a
markdown code fence via the shared `json_fences.strip_code_fences` helper (proactively
applied here -- this is a brand-new module and should not reintroduce the same "real models
sometimes fence JSON despite instructions" gap that `segment.py` had to close as forwarded
finding F1), then `json.loads` and validate.

`strip_code_fences` lives in the top-level `json_fences` module (a sibling of `ingestion`,
`query`, and `llm`), not inside `ingestion` itself -- issue #55 subtask 4.5.17.2 relocated it
there from the private `ingestion._json_fences`, since this module importing a private,
underscore-prefixed symbol from a different top-level package was itself a layering defect.

Exception design -- disclosed choice
----------------------------------------
`IntentRefinerError` is a *new* base exception (not a subclass of `llm.client.LLMError`):
`LLMError` means "the provider call itself failed"; the failures this module raises are the
opposite case -- the provider call *succeeded* and returned a string, but that string is not
a valid intent-refinement result. A single concrete subclass, `IntentRefinerParseError`,
covers every malformed-output case (unparseable JSON, missing/mistyped field, invalid
`query_type` value) with a specific, descriptive message identifying which check failed --
matching `segment.py`'s own precedent and this issue's own test-spec wording ("assert output
shape").
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import TYPE_CHECKING, Literal, Sequence

from json_fences import strip_code_fences

if TYPE_CHECKING:
    from llm.client import LLMClient

#: The two `query_type` values this module defines. See module docstring.
QueryType = Literal["factual_lookup", "broad_exploratory"]

_VALID_QUERY_TYPES: frozenset[str] = frozenset({"factual_lookup", "broad_exploratory"})

#: The complete set of required top-level keys in the LLM's JSON response.
_REQUIRED_STRING_FIELDS = ("refined_intent", "query_type")
_REQUIRED_LIST_FIELDS = ("entities",)
_REQUIRED_FIELDS = _REQUIRED_STRING_FIELDS + _REQUIRED_LIST_FIELDS

_INTENT_PROMPT_TEMPLATE = """You are a query intent-refinement assistant for a knowledge-base
search system. Given the user's raw query and a short conversation history, restate the
user's underlying intent clearly, extract any named entities mentioned, and classify the
query's type.

Conversation history (oldest first, may be empty):
{history_block}

Raw query:
---
{query}
---

Respond with ONLY a single JSON object (no prose, no markdown code fences) with exactly
these keys:
- "refined_intent": a clear, self-contained restatement of what the user is actually asking
  for, resolving any pronouns/references using the history above.
- "entities": a JSON array of entity name strings mentioned in the query or history that are
  relevant to answering it.
- "query_type": either the literal string "factual_lookup" (the user wants a specific,
  narrow fact or answer that likely exists in one or a few places) or the literal string
  "broad_exploratory" (the user wants a broad overview, summary, or exploration of a topic
  spanning many sources).
"""


class IntentRefinerError(Exception):
    """Base exception for this module's own intent-refinement failures.

    Deliberately NOT a subclass of `llm.client.LLMError`: `LLMError` means the provider call
    itself failed; this module's exceptions mean the call succeeded but its output could not
    be turned into a valid result. See the module docstring's "Exception design" section.
    """


class IntentRefinerParseError(IntentRefinerError):
    """Raised when the LLM's completion string is not a valid intent-refinement result.

    Covers every malformed-output case: unparseable JSON, a missing required field, a field
    of the wrong type, or an out-of-range `query_type` enum value. The exception message
    always identifies which specific check failed.
    """


@dataclass(frozen=True)
class IntentRefinerResult:
    """A single validated intent-refinement result, per `docs/LLD/query-agent.md`'s
    "`intent_refiner.py`" output shape.

    Attributes:
        refined_intent: A clear, self-contained restatement of the user's underlying intent.
        entities: Entity name strings mentioned in the query/history. See module docstring's
            "`history` shape" disclosure for why this is `list[str]`, not a richer per-entity
            record.
        query_type: `"factual_lookup"` or `"broad_exploratory"`. See module docstring's
            "`query_type` taxonomy" disclosure.
    """

    refined_intent: str
    entities: list[str]
    query_type: QueryType


def _build_prompt(query: str, history: Sequence[str]) -> str:
    """Render the intent-refinement prompt embedding `query` and `history`."""
    if history:
        history_block = "\n".join(f"- {turn}" for turn in history)
    else:
        history_block = "(no prior history)"
    return _INTENT_PROMPT_TEMPLATE.format(history_block=history_block, query=query)


def _require_type(payload: dict, field: str, expected_type: type, type_name: str) -> object:
    value = payload[field]
    if not isinstance(value, expected_type):
        raise IntentRefinerParseError(
            f"intent_refiner: field {field!r} must be a {type_name}, got "
            f"{type(value).__name__}: {value!r}"
        )
    return value


def _parse_intent_json(raw: str) -> IntentRefinerResult:
    """Parse and validate `raw` (the LLM's completion string) into an `IntentRefinerResult`.

    Raises:
        IntentRefinerParseError: On any parse or validation failure. See the class docstring
            for the exact failure cases covered.
    """
    stripped = strip_code_fences(raw)
    try:
        payload = json.loads(stripped)
    except json.JSONDecodeError as exc:
        raise IntentRefinerParseError(
            f"intent_refiner: LLM response is not valid JSON: {exc}"
        ) from exc

    if not isinstance(payload, dict):
        raise IntentRefinerParseError(
            f"intent_refiner: LLM response must be a JSON object, got "
            f"{type(payload).__name__}"
        )

    missing = [field for field in _REQUIRED_FIELDS if field not in payload]
    if missing:
        raise IntentRefinerParseError(
            f"intent_refiner: LLM response missing required field(s): {', '.join(missing)}"
        )

    for field in _REQUIRED_STRING_FIELDS:
        _require_type(payload, field, str, "string")
    for field in _REQUIRED_LIST_FIELDS:
        value = _require_type(payload, field, list, "list")
        for i, item in enumerate(value):
            if not isinstance(item, str):
                raise IntentRefinerParseError(
                    f"intent_refiner: field {field!r}[{i}] must be a string, got "
                    f"{type(item).__name__}: {item!r}"
                )

    query_type = payload["query_type"]
    if query_type not in _VALID_QUERY_TYPES:
        raise IntentRefinerParseError(
            f"intent_refiner: field 'query_type' must be one of "
            f"{sorted(_VALID_QUERY_TYPES)}, got {query_type!r}"
        )

    return IntentRefinerResult(
        refined_intent=payload["refined_intent"],
        entities=list(payload["entities"]),
        query_type=query_type,
    )


def refine_intent(
    query: str,
    history: Sequence[str],
    llm_client: "LLMClient",
    *,
    model: str | None = None,
    temperature: float = 0.0,
    max_tokens: int | None = None,
    timeout: float | None = None,
) -> IntentRefinerResult:
    """Refine `query` (with `history` context) by calling `llm_client` and parsing its output.

    Args:
        query: The user's raw query text.
        history: Short conversation history as prior raw utterance strings, most-recent-last
            (see module docstring for why this is `Sequence[str]`, not a richer schema).
        llm_client: The `LLMClient` used to perform the completion call.
        model, temperature, max_tokens, timeout: Forwarded verbatim to
            `llm_client.complete()`.

    Returns:
        A validated `IntentRefinerResult`.

    Raises:
        LLMError: Propagated unwrapped if `llm_client.complete()` itself fails (provider call
            failure) -- not converted into `IntentRefinerParseError`, since that means
            something different (see module docstring).
        IntentRefinerParseError: If the LLM's completion string is not a valid
            intent-refinement result (unparseable JSON, missing/mistyped field, or an invalid
            `query_type` value).
    """
    prompt = _build_prompt(query, history)
    raw = llm_client.complete(
        prompt,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=timeout,
    )
    return _parse_intent_json(raw)
