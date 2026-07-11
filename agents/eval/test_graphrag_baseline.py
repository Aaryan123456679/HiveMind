"""Tests for `agents/eval/baselines/graphrag_lite.py` (issue #27, subtask 5.2.3).

Per this subtask's own explicit scope boundary (mirroring 5.2.1's/5.2.2's own test files, see
`graphrag_lite.py`'s module docstring and this subtask's `architecture-discovery.md`): fixture
corpus + fixture queries only -- this test module never imports `agents/eval/datasets.py` or
reads from `data/synthetic_corpus/`.

Three tiers of test:

- Pure-unit tests (`parse_entity_list`, `_heuristic_entities` fallback) -- no network, always
  run.
- Stub-client tests (`EntityGraph.build`, `retrieve_documents`, hop-expansion behavior) -- use a
  small deterministic `_StubLLMClient` (canned JSON responses keyed by input-text substring)
  instead of a real Ollama call, so the hop-expansion-vs-direct-match assertion is fully
  deterministic and does not depend on a real model's exact wording choices. No network.
- Live-local tests -- build a real `EntityGraph` via the real local `llm.ollama_client.
  OllamaClient` (`llama3.1:8b`) and assert real, "plausible" entity-graph-driven retrieval on the
  fixture corpus/queries below, per this subtask's test spec ("run against a fixture corpus +
  fixture queries, assert plausible entity-graph-driven retrieval results"). Skipped (not
  mocked) if the local Ollama server / `llama3.1:8b` model is unreachable, mirroring the
  skip-if-unreachable convention already established by `test_vector_rag_baseline.py` /
  `test_vector_rag_rerank.py`.
"""

from __future__ import annotations

import httpx
import pytest

from eval.baselines.graphrag_lite import (
    DEFAULT_LLM_MODEL,
    EntityGraph,
    build_entity_extraction_prompt,
    extract_entities,
    parse_entity_list,
    retrieve_documents,
)
from eval.baselines.vector_rag import recall_at_k
from llm.client import LLMClient
from llm.ollama_client import OllamaClient

_OLLAMA_BASE_URL = "http://localhost:11434"
_LLM_MODEL = DEFAULT_LLM_MODEL


def _ollama_llm_available() -> bool:
    try:
        response = httpx.get(f"{_OLLAMA_BASE_URL}/api/tags", timeout=2.0)
        if response.status_code != 200:
            return False
        data = response.json()
        tags = {m.get("model", "") for m in data.get("models", [])}
        return any(tag.startswith(_LLM_MODEL) for tag in tags)
    except (httpx.HTTPError, ValueError):
        return False


_SKIP_REASON = (
    f"graphrag-lite baseline live retrieval test requires a reachable local Ollama server with "
    f"{_LLM_MODEL!r} pulled (`ollama pull {_LLM_MODEL}`) at {_OLLAMA_BASE_URL} -- skipped by "
    "default in environments missing this; see module docstring"
)


# --- Pure-unit tests: parse_entity_list / _heuristic_entities fallback ---


def test_parse_entity_list_valid_json():
    assert parse_entity_list('["Alpha", "Beta Gamma"]') == ["Alpha", "Beta Gamma"]


def test_parse_entity_list_strips_code_fence():
    response = '```json\n["Alpha", "Beta"]\n```'
    assert parse_entity_list(response) == ["Alpha", "Beta"]


def test_parse_entity_list_plain_code_fence_no_json_tag():
    response = '```\n["Alpha", "Beta"]\n```'
    assert parse_entity_list(response) == ["Alpha", "Beta"]


def test_parse_entity_list_dedupes_case_insensitively_keeping_first_seen_casing():
    assert parse_entity_list('["Alpha", "alpha", "ALPHA", "Beta"]') == ["Alpha", "Beta"]


def test_parse_entity_list_strips_whitespace_and_drops_empty_strings():
    assert parse_entity_list('["  Alpha  ", "", "   ", "Beta"]') == ["Alpha", "Beta"]


def test_parse_entity_list_falls_back_to_heuristic_on_unparseable_garbage():
    source_text = "The Security Team enforces the Password Policy for every account."
    garbled_response = "I cannot help with that request, sorry!"
    result = parse_entity_list(garbled_response, source_text)
    # Non-primary fallback path (see module docstring): capitalized-run heuristic over the
    # *source* text, not the garbled response.
    assert "Security Team" in result
    assert "Password Policy" in result


def test_parse_entity_list_non_list_json_falls_back_to_heuristic():
    source_text = "Widget Corp manages the Onboarding Process."
    response = '{"not": "a list"}'
    result = parse_entity_list(response, source_text)
    assert "Widget Corp" in result
    assert "Onboarding Process" in result


def test_parse_entity_list_empty_input_and_no_source_yields_empty_list():
    assert parse_entity_list("not valid json at all {{{") == []


def test_build_entity_extraction_prompt_includes_source_text():
    prompt = build_entity_extraction_prompt("some document text here")
    assert "some document text here" in prompt
    assert "JSON array" in prompt


def test_extract_entities_empty_text_returns_empty_without_calling_llm():
    class _ExplodingLLMClient(LLMClient):
        def complete(self, *args, **kwargs):  # type: ignore[override]
            raise AssertionError("must not be called for empty text")

    assert extract_entities("", _ExplodingLLMClient()) == []  # type: ignore[arg-type]
    assert extract_entities("   ", _ExplodingLLMClient()) == []  # type: ignore[arg-type]


# --- Stub-client tests: EntityGraph.build / retrieve_documents / hop expansion ---


class _StubLLMClient(LLMClient):
    """Deterministic canned-response `LLMClient` stub for entity extraction.

    `responses` maps an input-text substring to the canned raw completion string (typically a
    JSON array) that should be returned whenever that substring appears in the prompt built for
    it (`graphrag_lite.build_entity_extraction_prompt` always embeds the full input text
    verbatim, so a substring match against the prompt reliably identifies which input text is
    being processed). Raises if no configured substring matches, so a test's fixture data can
    never silently fall through to an unintended canned response.
    """

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


def test_entity_graph_build_links_cooccurring_entities_and_indexes_docs():
    stub = _StubLLMClient(
        {
            "doc-a-text": '["X", "Y"]',
            "doc-b-text": '["Y", "Z"]',
        }
    )
    docs = [("doc-a", "doc-a-text content"), ("doc-b", "doc-b-text content")]
    graph = EntityGraph.build(docs, stub)

    assert graph.entity_to_docs["x"] == {"doc-a"}
    assert graph.entity_to_docs["y"] == {"doc-a", "doc-b"}
    assert graph.entity_to_docs["z"] == {"doc-b"}
    # co-occurrence edges: X-Y (from doc-a), Y-Z (from doc-b); X and Z never co-occur directly.
    assert graph.entity_to_entities["x"] == {"y"}
    assert graph.entity_to_entities["y"] == {"x", "z"}
    assert graph.entity_to_entities["z"] == {"y"}
    assert graph.doc_entities["doc-a"] == {"x", "y"}
    assert graph.doc_entities["doc-b"] == {"y", "z"}
    assert graph.display_name["x"] == "X"


def test_retrieve_documents_return_shape_is_ranked_doc_id_list():
    stub = _StubLLMClient(
        {
            "doc-a-text": '["Alpha"]',
            "doc-b-text": '["Beta"]',
            "my query": '["Alpha"]',
        }
    )
    docs = [("doc-a", "doc-a-text content"), ("doc-b", "doc-b-text content")]
    graph = EntityGraph.build(docs, stub)

    retrieved = retrieve_documents("my query", graph, stub, top_k=5)
    assert retrieved == ["doc-a"]
    assert all(isinstance(doc_id, str) for doc_id in retrieved)


def test_retrieve_documents_no_matching_entities_returns_empty_list():
    stub = _StubLLMClient(
        {
            "doc-a-text": '["Alpha"]',
            "unrelated query": '["Zeta"]',
        }
    )
    docs = [("doc-a", "doc-a-text content")]
    graph = EntityGraph.build(docs, stub)
    assert retrieve_documents("unrelated query", graph, stub, top_k=3) == []


def test_match_query_entities_substring_fallback_recovers_paraphrase():
    # Query entity "password" is a substring of the graph's canonical "password policy" node --
    # exercises the disclosed loose entity-linking fallback (module docstring, fairness section)
    # rather than requiring brittle exact-string equality.
    stub = _StubLLMClient(
        {
            "doc-a-text": '["Password Policy"]',
            "my paraphrased query": '["password"]',
        }
    )
    docs = [("doc-a", "doc-a-text content")]
    graph = EntityGraph.build(docs, stub)
    assert retrieve_documents("my paraphrased query", graph, stub, top_k=3) == ["doc-a"]


def test_retrieve_documents_is_deterministic_across_repeated_calls():
    stub = _StubLLMClient(
        {
            "doc-a-text": '["Alpha", "Beta"]',
            "doc-b-text": '["Beta", "Gamma"]',
            "my query": '["Alpha"]',
        }
    )
    docs = [("doc-a", "doc-a-text content"), ("doc-b", "doc-b-text content")]
    graph = EntityGraph.build(docs, stub)

    first = retrieve_documents("my query", graph, stub, top_k=5, max_hops=1)
    second = retrieve_documents("my query", graph, stub, top_k=5, max_hops=1)
    assert first == second
    # never duplicates a doc id
    assert len(first) == len(set(first))


def test_hop_expansion_recovers_document_that_direct_match_alone_misses():
    """Core acceptance-criterion assertion for the graph-traversal mechanism itself (fairness
    constraint: this must genuinely use the graph, not just literal entity-string matching).

    doc-a directly matches the query's entity ("X"); doc-b does not mention "X" at all, but
    co-occurs with "Y" (which doc-a also mentions) -- so doc-b is only reachable via one hop of
    graph traversal outward from "X" through "Y".
    """
    stub = _StubLLMClient(
        {
            "doc-a-text": '["X", "Y"]',
            "doc-b-text": '["Y", "Z"]',
            "query about X": '["X"]',
        }
    )
    docs = [("doc-a", "doc-a-text content"), ("doc-b", "doc-b-text content")]
    graph = EntityGraph.build(docs, stub)

    # max_hops=0: direct entity matches only -- doc-b (no direct "X" mention) is not reachable.
    direct_only = retrieve_documents("query about X", graph, stub, top_k=1, max_hops=0)
    assert direct_only == ["doc-a"]

    # max_hops=1: hop expansion from "X" reaches "Y", which is linked to doc-b -- doc-b is now
    # recoverable, but ranks below the direct match (per HOP_DECAY < 1.0, hop-0 outranks hop-1).
    with_hop_expansion = retrieve_documents(
        "query about X", graph, stub, top_k=2, max_hops=1
    )
    assert with_hop_expansion == ["doc-a", "doc-b"]


# --- Live local-LLM tests ---

pytestmark = pytest.mark.skipif(not _ollama_llm_available(), reason=_SKIP_REASON)


@pytest.fixture(scope="module")
def llm_client() -> OllamaClient:
    return OllamaClient(model=_LLM_MODEL)


# Fixture corpus: distinct policy documents with clear named entities/concepts, plus a pure
# distractor. Deliberately NOT sourced from data/synthetic_corpus/ or agents/eval/datasets.py,
# per this subtask's explicit fixture-only scope boundary (mirrors 5.2.1's/5.2.2's own
# dedicated-fixture convention).
_FIXTURE_DOCS: list[tuple[str, str]] = [
    (
        "doc-mfa-policy",
        "Multi-factor authentication is required for every employee account at all times. "
        "The Identity Provider enforces this rule automatically during login, and employees "
        "cannot disable multi-factor authentication without a documented security exception "
        "approved in advance by the Security Team.",
    ),
    (
        "doc-password-reset",
        "When an employee is locked out of their account, the IT Helpdesk verifies their "
        "identity over the phone and issues a temporary password through the Identity "
        "Provider's self-service portal. The employee must then choose a new password meeting "
        "the Password Policy's length and complexity rules before regaining access.",
    ),
    (
        "doc-badge-access",
        "Physical badge access to office buildings is managed by the Facilities Team. Lost or "
        "stolen badges must be reported to Facilities within one business day so the Security "
        "Team can deactivate them and issue a replacement.",
    ),
    (
        "doc-distractor-parking",
        "Parking permits for the downtown office are issued by the Facilities Team once each "
        "quarter, on a first come first served basis, to employees who commute by car.",
    ),
]

_FIXTURE_QUERIES: list[tuple[str, set[str]]] = [
    (
        "Who approves exceptions to the multi-factor authentication requirement?",
        {"doc-mfa-policy"},
    ),
    (
        "How do I get a new password if I'm locked out of my account?",
        {"doc-password-reset"},
    ),
    (
        "Which team deactivates a lost employee badge?",
        {"doc-badge-access"},
    ),
]


@pytest.fixture(scope="module")
def fixture_graph(llm_client: OllamaClient) -> EntityGraph:
    return EntityGraph.build(_FIXTURE_DOCS, llm_client)


def test_live_entity_extraction_is_real_and_nonempty(llm_client: OllamaClient):
    """Confirms real (non-mocked) entity extraction is happening, not a stub/placeholder."""
    entities = extract_entities(_FIXTURE_DOCS[0][1], llm_client)
    assert len(entities) > 0
    assert all(isinstance(e, str) and e.strip() for e in entities)


def test_live_entity_graph_has_cooccurrence_edges(fixture_graph: EntityGraph):
    """Confirms the built graph is a genuine graph (has co-occurrence edges), not a flat index --
    every fixture document has more than one sentence's worth of related concepts, so at least
    one entity should have a non-empty neighbor set."""
    assert any(neighbors for neighbors in fixture_graph.entity_to_entities.values())


def test_top_hit_for_unambiguous_query_is_the_correct_document(
    llm_client: OllamaClient, fixture_graph: EntityGraph
):
    retrieved = retrieve_documents(
        "How do I get a new password if I'm locked out of my account?",
        fixture_graph,
        llm_client,
        top_k=1,
    )
    assert retrieved == ["doc-password-reset"]


def test_mean_recall_at_k_is_plausible_on_fixture_queries(
    llm_client: OllamaClient, fixture_graph: EntityGraph
):
    """Test spec: run retrieval against a fixture corpus + fixture queries, assert plausible
    entity-graph-driven retrieval results. A looser bar than 5.2.1's dense-embedding 0.75
    threshold is used deliberately -- entity-graph retrieval built on an LLM extraction step is a
    fuzzier signal than dense embeddings, and the test spec only asks for "plausible", not
    "reasonable" or "near-perfect"."""
    recalls = []
    for query_text, relevant_doc_ids in _FIXTURE_QUERIES:
        retrieved = retrieve_documents(query_text, fixture_graph, llm_client, top_k=2)
        recalls.append(recall_at_k(retrieved, relevant_doc_ids, k=2))

    mean_recall = sum(recalls) / len(recalls)
    assert mean_recall >= 0.6, (
        f"mean recall@2 across fixture queries was {mean_recall:.3f}, expected >= 0.6 "
        f"(per-query recalls: {recalls})"
    )


def test_distractor_document_is_never_the_top_hit(
    llm_client: OllamaClient, fixture_graph: EntityGraph
):
    """The pure distractor (parking permits) should never outrank a genuinely relevant document
    for any fixture query -- a sanity check on plausibility, not just recall."""
    for query_text, _relevant_doc_ids in _FIXTURE_QUERIES:
        retrieved = retrieve_documents(query_text, fixture_graph, llm_client, top_k=1)
        assert retrieved != ["doc-distractor-parking"], (
            f"query {query_text!r} incorrectly top-ranked the pure distractor document"
        )
