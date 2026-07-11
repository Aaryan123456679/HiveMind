"""Optional live-Ollama classification-accuracy test for `refine_intent()` (issue #55,
subtask 4.5.17.1).

Per the issue's test spec: run `refine_intent()` against real, unmocked query text
spanning both `factual_lookup` and `broad_exploratory` categories, asserting reasonable
classification accuracy -- not just parse success. Every test in this module is skipped
automatically (via `pytest.mark.skipif`, not an error/failure) unless a real Ollama
server is reachable at `OllamaClient`'s default base URL -- normal `pytest` runs and CI
never depend on a live Ollama server; run manually with ``ollama serve`` (and
``ollama pull llama3.1:8b``, or override `HIVEMIND_OLLAMA_MODEL`) running locally to
actually exercise these. This mirrors `agents/ingestion/test_segment_live.py`'s
established convention exactly (same probe, same skip mechanism, same env var).

Why this exists alongside the fully-mocked `test_intent_refiner.py`/
`test_intent_refiner_types.py`
------------------------------------------------------------------------------------------
`refine_intent()` has no classification logic of its own -- `query_type` is threaded
straight through from whatever the LLM returns (see `intent_refiner.py`'s own module
docstring). Every other test in `agents/query/` mocks `LLMClient.complete()` with a
hand-written canned string that is *told the expected `query_type` in advance*, so those
tests can only prove "the JSON round-trips through parsing correctly," never "the model
actually classified this query text correctly." That gap was forward-flagged in
`.cdr/memory/pending.md` and `.cdr/index/regression.jsonl`
(`hivemind-issue22-4.3.2-F1-parse-fidelity-not-classification`, `F-4.3.1-*`) as needing an
explicit decision. The decision (recorded in this run's `architecture-discovery.md`): yes,
a dedicated live test is warranted, and this module is it -- it calls a real Ollama model
with genuinely different, real query text and asserts the returned `query_type` matches
the category a human would expect, which no mocked test can do by construction.

Availability check -- disclosed design
-----------------------------------------
`_ollama_is_reachable()` does a short-timeout plain HTTP GET against
`{base_url}/` (Ollama's root endpoint responds `200 Ollama is running` with no auth
needed) and treats any connection failure/timeout as "not available" -- this is a
liveness probe only, not a correctness check; if it responds we still let the actual
`OllamaClient` calls below raise/fail normally on any real error.

Accuracy assertion -- disclosed design
-----------------------------------------
Real LLM output is not deterministic verbatim text, so this module does not assert exact
`refined_intent`/`entities` values -- only that `query_type` lands on the expected one of
the two closed-taxonomy values for each query, plus basic non-empty/shape invariants on
the rest of the result. Two queries are used, one clearly narrow/factual and one clearly
broad/exploratory, each unambiguous enough that a reasonably capable model should not
plausibly misclassify it; a single miss on either is treated as a real failure (not
flaky-tolerated), since the whole point of this test is to catch exactly that.
"""

from __future__ import annotations

import os

import httpx
import pytest

from llm.ollama_client import DEFAULT_BASE_URL, OllamaClient
from query.intent_refiner import IntentRefinerResult, refine_intent

_MODEL = os.environ.get("HIVEMIND_OLLAMA_MODEL", "llama3.1:8b")
_PROBE_TIMEOUT_SECONDS = 1.0


def _ollama_is_reachable(base_url: str = DEFAULT_BASE_URL) -> bool:
    """Return True iff an Ollama server responds at `base_url` within a short timeout.

    See module docstring's "Availability check" section. Any exception (connection
    refused, DNS failure, timeout, ...) is treated as "not reachable" -- this probe
    must never itself raise, or every normal (no-Ollama) test run would error instead
    of skipping.
    """
    try:
        response = httpx.get(base_url, timeout=_PROBE_TIMEOUT_SECONDS)
        return response.status_code == 200
    except httpx.HTTPError:
        return False


_SKIP_REASON = (
    f"no Ollama server reachable at {DEFAULT_BASE_URL} -- this classification-accuracy "
    "test is optional and skipped by default; run `ollama serve` (+ `ollama pull "
    f"{_MODEL}`) locally to exercise it"
)

pytestmark = pytest.mark.skipif(not _ollama_is_reachable(), reason=_SKIP_REASON)


@pytest.fixture()
def ollama_client() -> OllamaClient:
    return OllamaClient(model=_MODEL)


def test_refine_intent_classifies_factual_lookup_query(
    ollama_client: OllamaClient,
) -> None:
    """A narrow, specific-fact query against a real Ollama model is classified
    `factual_lookup` -- the actual classification-accuracy check, not just parse
    success (which `test_intent_refiner.py`'s mocked tests already cover).
    """
    query = "What is the maximum number of GraphNeighbors hops the topic selector can request?"

    result = refine_intent(query, [], ollama_client, temperature=0.0)

    assert isinstance(result, IntentRefinerResult)
    assert result.query_type == "factual_lookup"
    assert isinstance(result.refined_intent, str)
    assert result.refined_intent.strip() != ""
    assert isinstance(result.entities, list)


def test_refine_intent_classifies_broad_exploratory_query(
    ollama_client: OllamaClient,
) -> None:
    """A broad, open-ended overview query against a real Ollama model is classified
    `broad_exploratory` -- the actual classification-accuracy check, not just parse
    success.
    """
    query = (
        "Give me a broad overview of everything you can tell me about renewable energy."
    )

    result = refine_intent(query, [], ollama_client, temperature=0.0)

    assert isinstance(result, IntentRefinerResult)
    assert result.query_type == "broad_exploratory"
    assert isinstance(result.refined_intent, str)
    assert result.refined_intent.strip() != ""
    assert isinstance(result.entities, list)
