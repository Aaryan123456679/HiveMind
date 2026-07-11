"""Tests `agents/eval/baselines/vector_rag_rerank.py` (issue #27, subtask 5.2.2).

Per this subtask's own explicit scope boundary (see `vector_rag_rerank.py`'s module docstring and
this subtask's `architecture-discovery.md`): fixture corpus + fixture query only -- this test does
not import `agents/eval/datasets.py` or read from `data/synthetic_corpus/`, and does not run any
real large-scale benchmark.

Two tiers, mirroring `test_vector_rag_baseline.py`'s convention:
- Pure-unit tests (`precision_at_k`, `parse_rerank_order`) -- no network, always run.
- Live-local tests -- build a real `VectorRagIndex` via the real local `OllamaEmbeddingClient`
  (`nomic-embed-text`) and rerank via the real local `OllamaClient` (`llama3.1:8b`); skipped (not
  mocked) if the local Ollama server or either model is unreachable, mirroring the skip-if-
  unreachable convention already established by `agents/ingestion/test_e2e_smoke.py` and
  `test_vector_rag_baseline.py`.

Fixture design -- disclosed
-------------------------------
Per `architecture-discovery.md` section 4, this fixture is deliberately its own dedicated corpus
(not 5.2.1's, which is already near-perfectly ordered by plain vector search and so would not
discriminate). The query asks about resetting an email password. `doc-reset` is the true answer
but is phrased with no literal overlap with "password"/"reset"/"email account" (paraphrased as
"regaining entry to a locked mailbox" / "temporary secret phrase"). `doc-onboarding` is an
off-topic distractor (new-hire account setup, not a reset) that repeats the query's literal
vocabulary ("email account", "password") heavily. This was verified empirically against the real
local embedding model (see this subtask's `self-consistency.json`) to genuinely fool
cosine-similarity-only ranking into ranking `doc-onboarding` above `doc-reset` -- not merely
asserted to do so.
"""

from __future__ import annotations

import httpx
import pytest

from eval.baselines.vector_rag import Chunk, OllamaEmbeddingClient, VectorRagIndex, chunk_document
from eval.baselines.vector_rag_rerank import (
    RerankCandidate,
    build_rerank_prompt,
    parse_rerank_order,
    precision_at_k,
    rerank_documents,
    retrieve_documents_reranked,
)
from llm.ollama_client import OllamaClient

_OLLAMA_BASE_URL = "http://localhost:11434"
_EMBEDDING_MODEL = "nomic-embed-text"
_LLM_MODEL = "llama3.1:8b"


def _ollama_and_models_available() -> bool:
    try:
        response = httpx.get(f"{_OLLAMA_BASE_URL}/api/tags", timeout=2.0)
        if response.status_code != 200:
            return False
        data = response.json()
        tags = {m.get("model", "") for m in data.get("models", [])}
        has_embedding = any(tag.startswith(_EMBEDDING_MODEL) for tag in tags)
        has_llm = any(tag.startswith(_LLM_MODEL) for tag in tags)
        return has_embedding and has_llm
    except (httpx.HTTPError, ValueError):
        return False


_SKIP_REASON = (
    f"vector-RAG reranking live-local tests require a reachable local Ollama server at "
    f"{_OLLAMA_BASE_URL} with both {_EMBEDDING_MODEL!r} and {_LLM_MODEL!r} pulled "
    f"(`ollama pull {_EMBEDDING_MODEL}`, `ollama pull {_LLM_MODEL}`)."
)

# --- Dedicated discriminating fixture (see module docstring) ---

_FIXTURE_DOCS: dict[str, str] = {
    "doc-reset": (
        "Regaining entry to a locked mailbox. When a staff member is locked out and cannot sign "
        "in, the help desk verifies their identity through a callback phone number, then issues "
        "a fresh temporary secret phrase so they can get back into their inbox and set a new one "
        "of their own choosing."
    ),
    "doc-onboarding": (
        "New employee email account setup and password policy. Every new hire receives a company "
        "email account on day one and must choose a password for that account meeting length and "
        "complexity rules, then enroll the account in multi factor authentication before the "
        "account is activated."
    ),
    "doc-benefits": (
        "Health insurance enrollment window opens each fall. Employees choose a medical plan, a "
        "dental plan, and a vision plan, and may add dependents during the open enrollment "
        "period."
    ),
    "doc-parking": (
        "Parking permits are issued by facilities on a first come basis each quarter for "
        "employees who commute by car to the downtown office building."
    ),
}

_FIXTURE_QUERY = "How do I reset the password for my email account?"
_FIXTURE_RELEVANT = {"doc-reset"}

# `doc-reset` ranks 2nd by plain cosine similarity within this pool (verified empirically, see
# self-consistency.json), so a candidate pool of 3 is enough to make it recoverable by reranking
# while a `top_k`-only (no rerank) retrieval of size 1 misses it.
_CANDIDATE_POOL_SIZE = 3
_TOP_K = 1


# --- Pure-unit tests: precision_at_k ---


def test_precision_at_k_all_relevant():
    assert precision_at_k(["a", "b"], {"a", "b"}, k=2) == 1.0


def test_precision_at_k_none_relevant():
    assert precision_at_k(["a", "b"], {"c"}, k=2) == 0.0


def test_precision_at_k_partial():
    assert precision_at_k(["a", "b"], {"a"}, k=2) == 0.5


def test_precision_at_k_only_considers_top_k():
    # "a" is relevant but sits outside the top-1 slice, so precision@1 must not count it.
    assert precision_at_k(["b", "a"], {"a"}, k=1) == 0.0


def test_precision_at_k_zero_k_is_vacuous():
    assert precision_at_k(["a"], {"a"}, k=0) == 1.0


def test_precision_at_k_empty_retrieved_list():
    assert precision_at_k([], {"a"}, k=1) == 0.0


# --- Pure-unit tests: parse_rerank_order ---


def test_parse_rerank_order_well_formed():
    assert parse_rerank_order("3, 1, 2", num_candidates=3) == [3, 1, 2]


def test_parse_rerank_order_with_extra_commentary():
    response = "Sure! Based on relevance, the ranking is: 2, 1, 3. Hope that helps."
    assert parse_rerank_order(response, num_candidates=3) == [2, 1, 3]


def test_parse_rerank_order_deduplicates_keeping_first_occurrence():
    assert parse_rerank_order("2, 2, 1, 3", num_candidates=3) == [2, 1, 3]


def test_parse_rerank_order_ignores_out_of_range_numbers():
    assert parse_rerank_order("5, 2, 1, 0", num_candidates=3) == [2, 1, 3]


def test_parse_rerank_order_appends_missing_candidates_in_original_order():
    # Candidate 2 never mentioned -- must still appear, at the end, per the no-drop guarantee.
    assert parse_rerank_order("3, 1", num_candidates=3) == [3, 1, 2]


def test_parse_rerank_order_completely_unparseable_falls_back_to_original_order():
    assert parse_rerank_order("I cannot help with that.", num_candidates=3) == [1, 2, 3]


def test_parse_rerank_order_is_always_a_full_permutation():
    for response in ["", "not numbers", "1", "1, 1, 1", "9, 8, 7"]:
        order = parse_rerank_order(response, num_candidates=4)
        assert sorted(order) == [1, 2, 3, 4]


def test_build_rerank_prompt_includes_query_and_all_candidates():
    candidates = [
        RerankCandidate(doc_id="d1", text="alpha text"),
        RerankCandidate(doc_id="d2", text="beta text"),
    ]
    prompt = build_rerank_prompt("my query", candidates)
    assert "my query" in prompt
    assert "alpha text" in prompt
    assert "beta text" in prompt
    assert "[1]" in prompt and "[2]" in prompt


def test_rerank_documents_empty_candidates_returns_empty_without_calling_llm():
    class _ExplodingLLMClient:
        def complete(self, *args, **kwargs):
            raise AssertionError("must not be called for an empty candidate list")

    assert rerank_documents("q", [], _ExplodingLLMClient()) == []  # type: ignore[arg-type]


# --- Live-local tests ---


@pytest.mark.skipif(not _ollama_and_models_available(), reason=_SKIP_REASON)
class TestLiveLocalReranking:
    @pytest.fixture(scope="class")
    def embed_client(self) -> OllamaEmbeddingClient:
        return OllamaEmbeddingClient()

    @pytest.fixture(scope="class")
    def llm_client(self) -> OllamaClient:
        return OllamaClient(model=_LLM_MODEL)

    @pytest.fixture(scope="class")
    def fixture_index(self, embed_client: OllamaEmbeddingClient) -> VectorRagIndex:
        all_chunks: list[Chunk] = []
        for doc_id, text in _FIXTURE_DOCS.items():
            all_chunks.extend(chunk_document(doc_id, text, chunk_size_words=200, overlap_words=0))
        return VectorRagIndex.build(all_chunks, embed_client)

    def test_rerank_off_matches_plain_retrieve_documents(
        self, embed_client: OllamaEmbeddingClient, fixture_index: VectorRagIndex
    ):
        """`rerank=False` must be exactly equivalent to calling `retrieve_documents` directly,
        per this module's own disclosed design (see module docstring)."""
        from eval.baselines.vector_rag import retrieve_documents

        direct = retrieve_documents(_FIXTURE_QUERY, fixture_index, embed_client, top_k=_TOP_K)
        via_wrapper = retrieve_documents_reranked(
            _FIXTURE_QUERY,
            fixture_index,
            embed_client,
            top_k=_TOP_K,
            rerank=False,
        )
        assert via_wrapper == direct

    def test_precision_at_k_without_rerank_is_imperfect_on_fixture(
        self, embed_client: OllamaEmbeddingClient, fixture_index: VectorRagIndex
    ):
        """Confirms the discriminating case is real: plain vector search alone does not achieve
        perfect precision@k on this fixture (the off-topic distractor genuinely outranks the true
        answer). If this ever starts passing at 1.0 (e.g. a future embedding-model change), the
        fixture must be redesigned rather than this assertion loosened -- see
        architecture-discovery.md's non-fabrication gate."""
        retrieved = retrieve_documents_reranked(
            _FIXTURE_QUERY,
            fixture_index,
            embed_client,
            top_k=_TOP_K,
            rerank=False,
            candidate_pool_size=_CANDIDATE_POOL_SIZE,
        )
        baseline_precision = precision_at_k(retrieved, _FIXTURE_RELEVANT, _TOP_K)
        assert baseline_precision < 1.0, (
            f"expected the unreranked baseline to be imperfect on this deliberately "
            f"discriminating fixture, got precision@{_TOP_K}={baseline_precision:.3f} "
            f"(retrieved={retrieved!r})"
        )

    def test_reranking_improves_precision_at_k_over_baseline(
        self,
        embed_client: OllamaEmbeddingClient,
        llm_client: OllamaClient,
        fixture_index: VectorRagIndex,
    ):
        """Test spec: compare precision@k reranking on vs. off on a fixture set. This is the
        core acceptance-criterion assertion: reranking must *measurably improve* precision@k, not
        merely tie it."""
        without_rerank = retrieve_documents_reranked(
            _FIXTURE_QUERY,
            fixture_index,
            embed_client,
            top_k=_TOP_K,
            rerank=False,
            candidate_pool_size=_CANDIDATE_POOL_SIZE,
        )
        with_rerank = retrieve_documents_reranked(
            _FIXTURE_QUERY,
            fixture_index,
            embed_client,
            top_k=_TOP_K,
            rerank=True,
            llm_client=llm_client,
            candidate_pool_size=_CANDIDATE_POOL_SIZE,
            doc_texts=_FIXTURE_DOCS,
        )

        precision_off = precision_at_k(without_rerank, _FIXTURE_RELEVANT, _TOP_K)
        precision_on = precision_at_k(with_rerank, _FIXTURE_RELEVANT, _TOP_K)

        assert precision_on > precision_off, (
            f"expected reranking to measurably improve precision@{_TOP_K} on the fixture set, "
            f"got rerank-off={precision_off:.3f} (retrieved={without_rerank!r}) vs. "
            f"rerank-on={precision_on:.3f} (retrieved={with_rerank!r})"
        )
        # Concretely: the true answer should end up on top once reranked.
        assert with_rerank == ["doc-reset"]
