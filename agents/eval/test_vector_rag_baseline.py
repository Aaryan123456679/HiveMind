"""Tests for `agents/eval/baselines/vector_rag.py` (issue #27, subtask 5.2.1).

Per this subtask's own explicit scope boundary: fixture corpus + fixture queries only -- this
test module never imports `agents/eval/datasets.py` or reads from `data/synthetic_corpus/`. It
does not run any real large-scale benchmark; see module docstring in `vector_rag.py` and this
subtask's `architecture-discovery.md` for the full disclosed reasoning.

Two tiers of test:

- Pure-unit tests (`chunk_document`, `recall_at_k`) -- no network, always run.
- Live-local-embedding tests -- build a real `VectorRagIndex` via the real local
  `OllamaEmbeddingClient` (`nomic-embed-text`) and assert real, reasonable recall on the
  fixture corpus/queries below, per this subtask's test spec ("run retrieval against a fixture
  corpus + fixture queries, assert reasonable recall on known-relevant chunks"). These are
  skipped (not mocked) if the local Ollama server / `nomic-embed-text` model is unreachable,
  mirroring the skip-if-unreachable convention already established by
  `agents/ingestion/test_e2e_smoke.py`.
"""

from __future__ import annotations

import httpx
import pytest

from eval.baselines.vector_rag import (
    DEFAULT_OVERLAP_WORDS,
    DEFAULT_CHUNK_SIZE_WORDS,
    Chunk,
    OllamaEmbeddingClient,
    VectorRagIndex,
    chunk_document,
    recall_at_k,
    retrieve_documents,
    select_chunk_config,
)

_OLLAMA_BASE_URL = "http://localhost:11434"
_EMBEDDING_MODEL = "nomic-embed-text"


def _ollama_embeddings_available() -> bool:
    try:
        response = httpx.get(f"{_OLLAMA_BASE_URL}/api/tags", timeout=2.0)
        if response.status_code != 200:
            return False
        data = response.json()
        tags = {m.get("model", "") for m in data.get("models", [])}
        return any(tag.startswith(_EMBEDDING_MODEL) for tag in tags)
    except (httpx.HTTPError, ValueError):
        return False


_SKIP_REASON = (
    f"vector-RAG baseline live recall test requires a reachable local Ollama server with "
    f"{_EMBEDDING_MODEL!r} pulled (`ollama pull {_EMBEDDING_MODEL}`) at {_OLLAMA_BASE_URL} -- "
    "skipped by default in environments missing this; see module docstring"
)


# --- Fixture corpus: multi-section "handbook" documents plus pure distractors ---
# Deliberately NOT sourced from data/synthetic_corpus/ or agents/eval/datasets.py, per this
# subtask's explicit fixture-only scope boundary. Each handbook document deliberately packs
# several distinct policy sections into one document (mirroring how a real employee handbook
# reads), long enough (~350-450 words) that chunk-size/overlap choices materially affect
# whether a chunk isolates one topic cleanly or dilutes it by spanning a section boundary --
# this is what makes the tuning sweep in `select_chunk_config` discriminate between candidates
# rather than trivially tying (a risk with documents short enough to fit in a single chunk
# regardless of chunk size).
_FIXTURE_DOCS: list[tuple[str, str]] = [
    (
        "doc-handbook-ops",
        "Vacation policy. Employees accrue paid vacation time at a rate of one and a half "
        "days per month worked, credited automatically at the end of each pay period. "
        "Vacation requests must be submitted at least two weeks in advance through the HR "
        "portal, and manager approval is required before time off is confirmed on the "
        "shared calendar. Unused vacation days roll over up to a maximum of ten days into "
        "the next calendar year, after which any remaining balance above that cap is "
        "forfeited on January first. Part time employees accrue vacation on a prorated "
        "basis relative to their scheduled weekly hours. "
        "Expense reimbursement policy. All business travel expenses must be submitted for "
        "reimbursement within thirty days of the trip's completion using the expense "
        "management system. Receipts are required for any single expense over twenty five "
        "dollars, scanned or photographed clearly enough to read the vendor name and total. "
        "Meals during business travel are reimbursed up to a daily per diem cap set by "
        "destination city, and alcohol purchases are never reimbursable under any "
        "circumstance regardless of context. Reimbursements are typically processed within "
        "two pay cycles of approval. "
        "Parking policy. Employees who drive to the office may request a reserved parking "
        "badge from facilities, subject to availability at each office location. Parking "
        "badges are non transferable and must be returned upon termination of employment.",
    ),
    (
        "doc-handbook-security",
        "VPN access policy. Remote employees connecting to internal systems must use the "
        "company issued VPN client, configured with multi factor authentication before the "
        "first connection is permitted. VPN sessions automatically time out after eight "
        "hours of inactivity and require re-authentication to resume. Personal devices are "
        "not permitted to connect to the VPN without prior written approval from the "
        "security team, and any approved personal device is subject to periodic compliance "
        "scanning. "
        "Password policy. Account passwords must be at least twelve characters long and "
        "rotated every ninety days through the identity provider's self service portal. "
        "Password reuse across the last ten passwords is blocked automatically by the "
        "identity provider at rotation time. Multi factor authentication is mandatory for "
        "all accounts with access to customer data, and cannot be disabled by the account "
        "owner without a documented security exception. "
        "Badge access policy. Physical building badges grant access to common areas during "
        "business hours and to an employee's assigned floor at all times. Lost badges must "
        "be reported to facilities within one business day so they can be deactivated.",
    ),
    (
        "doc-handbook-people",
        "Parental leave policy. New parents are eligible for sixteen weeks of paid parental "
        "leave, which may be taken continuously or split into two blocks within the first "
        "year after birth or adoption. Parental leave requests should be submitted to HR at "
        "least thirty days before the expected start date whenever the timing is "
        "foreseeable, though later notice is accepted for unplanned circumstances. Parental "
        "leave runs concurrently with any applicable statutory leave rather than in "
        "addition to it. "
        "Onboarding policy. New hire onboarding spans the first two weeks of employment and "
        "includes account provisioning, security training, and a benefits enrollment "
        "session on day one. IT provisions laptop and VPN access on the employee's first "
        "day, and password setup, including multi factor authentication enrollment for all "
        "required accounts, happens during that same first day session alongside badge "
        "issuance. "
        "Performance review policy. Formal performance reviews occur twice a year, in "
        "spring and fall, combining self assessment, manager assessment, and peer feedback "
        "gathered through the HR system.",
    ),
    (
        "doc-distractor-facilities",
        "Shared kitchen etiquette. Employees using the shared office kitchen are asked to "
        "label perishable food with their name and date, and to clear out the refrigerator "
        "of unlabeled items every Friday afternoon. Dishes left in the sink overnight are "
        "discarded by the cleaning staff the following morning. Coffee machines are "
        "serviced weekly by facilities, and any malfunction should be reported through the "
        "facilities ticketing system rather than attempted repairs by staff. "
        "Conference room booking. Conference rooms are booked through the shared office "
        "calendar system on a first come first served basis, with a maximum booking length "
        "of two hours for rooms seating six or fewer people. Recurring meetings should be "
        "booked at least a week in advance to avoid displacing ad hoc bookings.",
    ),
    (
        "doc-distractor-benefits-extra",
        "Commuter benefits. Employees may enroll in a pre tax commuter benefits program "
        "covering public transit passes and qualified parking expenses up to the monthly "
        "IRS limit. Enrollment changes take effect the first day of the following month "
        "and can be made at any time through the benefits portal, not just during open "
        "enrollment. "
        "Gym reimbursement. A modest monthly gym membership reimbursement is available to "
        "all full time employees upon submission of a receipt through the expense system, "
        "capped well below typical premium membership rates.",
    ),
]

# --- Fixture queries with known relevant doc_ids ---
# doc-handbook-people deliberately overlaps doc-handbook-security's VPN/password topics via
# its onboarding section, matching the real synthetic corpus's own "cross-reference" design
# pattern. The two doc-distractor-* documents are never relevant to any query below --
# genuine negatives that a well-tuned baseline must not surface ahead of a real match.
_FIXTURE_QUERIES: list[tuple[str, set[str]]] = [
    ("What is the policy on vacation time accrual?", {"doc-handbook-ops"}),
    ("How are business travel expenses reimbursed?", {"doc-handbook-ops"}),
    (
        "What are the requirements for connecting to the VPN?",
        {"doc-handbook-security", "doc-handbook-people"},
    ),
    (
        "What are the password rotation and multi factor authentication rules?",
        {"doc-handbook-security", "doc-handbook-people"},
    ),
    ("How much paid parental leave do new parents get?", {"doc-handbook-people"}),
]


# --- Pure-unit tests: chunk_document ---


def test_chunk_document_basic_word_windows():
    text = " ".join(f"word{i}" for i in range(10))
    chunks = chunk_document("d1", text, chunk_size_words=4, overlap_words=1)

    assert all(isinstance(c, Chunk) for c in chunks)
    assert [c.text for c in chunks] == [
        "word0 word1 word2 word3",
        "word3 word4 word5 word6",
        "word6 word7 word8 word9",
    ]
    assert [(c.start_word, c.end_word) for c in chunks] == [(0, 4), (3, 7), (6, 10)]
    assert all(c.doc_id == "d1" for c in chunks)
    assert [c.chunk_id for c in chunks] == ["d1::chunk0", "d1::chunk1", "d1::chunk2"]


def test_chunk_document_never_bisects_a_word():
    text = "alpha beta gamma delta epsilon"
    for chunk in chunk_document("d2", text, chunk_size_words=2, overlap_words=0):
        for word in chunk.text.split():
            assert word in text.split()


def test_chunk_document_empty_text_yields_no_chunks():
    assert chunk_document("d3", "", chunk_size_words=5, overlap_words=1) == []
    assert chunk_document("d3", "   ", chunk_size_words=5, overlap_words=1) == []


def test_chunk_document_exact_multiple_of_chunk_size_no_dangling_empty_chunk():
    text = " ".join(f"w{i}" for i in range(8))
    chunks = chunk_document("d4", text, chunk_size_words=4, overlap_words=0)
    assert len(chunks) == 2
    assert chunks[-1].end_word == 8


def test_chunk_document_rejects_invalid_overlap():
    with pytest.raises(ValueError):
        chunk_document("d5", "a b c", chunk_size_words=4, overlap_words=4)
    with pytest.raises(ValueError):
        chunk_document("d5", "a b c", chunk_size_words=4, overlap_words=5)
    with pytest.raises(ValueError):
        chunk_document("d5", "a b c", chunk_size_words=0, overlap_words=0)


# --- Pure-unit tests: recall_at_k ---


def test_recall_at_k_perfect_and_partial():
    assert recall_at_k(["a", "b", "c"], {"a", "b"}, k=3) == 1.0
    assert recall_at_k(["a", "x", "y"], {"a", "b"}, k=3) == 0.5
    assert recall_at_k(["x", "y", "z"], {"a", "b"}, k=3) == 0.0


def test_recall_at_k_respects_k_cutoff():
    # relevant doc is present in the full list but outside the top-1 cutoff.
    assert recall_at_k(["x", "a"], {"a"}, k=1) == 0.0
    assert recall_at_k(["x", "a"], {"a"}, k=2) == 1.0


def test_recall_at_k_empty_relevant_set_is_vacuously_satisfied():
    assert recall_at_k(["a", "b"], set(), k=2) == 1.0


# --- Live local-embedding tests ---

pytestmark = pytest.mark.skipif(
    not _ollama_embeddings_available(), reason=_SKIP_REASON
)


@pytest.fixture(scope="module")
def embed_client() -> OllamaEmbeddingClient:
    return OllamaEmbeddingClient()


@pytest.fixture(scope="module")
def fixture_index(embed_client: OllamaEmbeddingClient) -> VectorRagIndex:
    all_chunks: list[Chunk] = []
    for doc_id, text in _FIXTURE_DOCS:
        all_chunks.extend(
            chunk_document(
                doc_id,
                text,
                chunk_size_words=DEFAULT_CHUNK_SIZE_WORDS,
                overlap_words=DEFAULT_OVERLAP_WORDS,
            )
        )
    return VectorRagIndex.build(all_chunks, embed_client)


def test_live_embeddings_are_real_nonzero_vectors(embed_client: OllamaEmbeddingClient):
    """Confirms real (non-mocked) embeddings are being produced, not a stub/placeholder."""
    vectors = embed_client.embed(["hello world", "a completely different sentence"])
    assert len(vectors) == 2
    assert len(vectors[0]) > 0
    assert vectors[0] != vectors[1]
    assert any(v != 0.0 for v in vectors[0])


def test_retrieve_documents_reasonable_recall_on_fixture_queries(
    embed_client: OllamaEmbeddingClient, fixture_index: VectorRagIndex
):
    """Test spec: run retrieval against a fixture corpus + fixture queries, assert reasonable
    recall on known-relevant chunks."""
    recalls = []
    for query_text, relevant_doc_ids in _FIXTURE_QUERIES:
        retrieved = retrieve_documents(
            query_text, fixture_index, embed_client, top_k=3
        )
        recalls.append(recall_at_k(retrieved, relevant_doc_ids, k=3))

    mean_recall = sum(recalls) / len(recalls)
    assert mean_recall >= 0.75, (
        f"mean recall@3 across fixture queries was {mean_recall:.3f}, expected >= 0.75 "
        f"(per-query recalls: {recalls})"
    )


def test_top_hit_for_unambiguous_query_is_the_correct_document(
    embed_client: OllamaEmbeddingClient, fixture_index: VectorRagIndex
):
    """A sanity check distinct from mean recall: for an unambiguous single-topic query, the
    single best-ranked document should be the genuinely relevant one, not merely present
    somewhere in the top-k."""
    retrieved = retrieve_documents(
        "What is the policy on vacation time accrual?",
        fixture_index,
        embed_client,
        top_k=1,
    )
    assert retrieved == ["doc-handbook-ops"]


def test_select_chunk_config_grid_search_matches_shipped_defaults(
    embed_client: OllamaEmbeddingClient,
):
    """Proves the shipped DEFAULT_CHUNK_SIZE_WORDS/DEFAULT_OVERLAP_WORDS constants really are
    the output of a real tuning sweep against this fixture corpus/query set (not just an
    asserted docstring claim) -- see architecture-discovery.md and self-consistency.json for
    the full per-candidate recall table this sweep produces."""
    winning_config, all_results = select_chunk_config(
        _FIXTURE_DOCS, _FIXTURE_QUERIES, embed_client, top_k=3
    )

    assert winning_config in all_results
    assert all(0.0 <= recall <= 1.0 for recall in all_results.values())
    assert winning_config == (DEFAULT_CHUNK_SIZE_WORDS, DEFAULT_OVERLAP_WORDS)
