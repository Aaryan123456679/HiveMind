"""End-to-end query-pipeline test: seed a small real corpus on disk, run a real query
through the full pipeline (`intent_refiner -> topic_selector -> synthesizer`), and assert
the synthesized answer's citations resolve to real files that exist in the seeded corpus
(no hallucinated citations pass silently).

Per issue #25 subtask 4.6.2's acceptance criteria ("a query against a small seeded corpus
returns an answer whose citations resolve to real files that exist in the corpus") and
test spec ("seeds a handful of topic files, runs a query through the full pipeline, and
asserts the returned citations are valid file paths matching content in the corpus").

Scope: what "end-to-end" means here -- disclosed
--------------------------------------------------
Per subtask 4.6.1 (`agents/query/pipeline.py`), `run_query_pipeline()` takes
`search_candidates`/`graph_neighbors`/`get_file` as injected callables plus an
`LLMClient` -- a disclosed, accepted DI seam; there is no real gRPC wiring in
`agents/query/` today (no `wiring.py` analogue, unlike `agents/ingestion/`). This module
does NOT stand up a real engine subprocess or a real Ollama server (contrast
`agents/ingestion/test_e2e_smoke.py`, which does exactly that for the ingestion side) --
that remains future work (see this run's handoff.json). "End-to-end" in this module
means: every non-LLM, non-gRPC step of the real pipeline (`select_top_k`,
`expand_insufficient_topics`, `combine_and_cap`, `_build_selected_markdown`, and
`synthesizer.py`'s own citation-resolution logic) runs for real and unmocked, against a
real, small corpus of files genuinely written to and read back from disk -- only the LLM
completion calls and the gRPC-boundary callables are fakes (mirroring `test_pipeline.py`'s
own established convention, per `test_pipeline.py`'s own module docstring), and those
fakes are themselves backed by real on-disk file content, not a hardcoded in-memory
fixture standing in for "the corpus."
"""

from __future__ import annotations

import json
from pathlib import Path

from llm.client import LLMClient
from query.pipeline import run_query_pipeline
from query.topic_selector import GraphNeighbor, TopicCandidate

#: The seeded corpus: (relative path, real distinct markdown content) pairs. Three
#: genuinely different topics so a keyword-overlap search can meaningfully distinguish
#: between them (see `_score` below).
_CORPUS_FILES: list[tuple[str, str]] = [
    (
        "billing/InvoiceDisputes.md",
        "# Invoice Disputes\n\n"
        "If a customer disputes an invoice or believes they were charged twice for the "
        "same order, open a dispute ticket within 30 days of the invoice date. Duplicate "
        "charges are refunded automatically once the dispute is confirmed by billing.\n",
    ),
    (
        "security/ApiKeyRotation.md",
        "# API Key Rotation Policy\n\n"
        "All API keys issued to external integrations must be rotated every 90 days. "
        "Rotation is performed by generating a new key, updating the integration's "
        "configured secret, then revoking the old key after a 24-hour grace period.\n",
    ),
    (
        "onboarding/NewHireChecklist.md",
        "# New Hire Onboarding Checklist\n\n"
        "New employees complete IT provisioning, benefits enrollment, and a security "
        "training module within their first week. Managers assign a buddy for the "
        "first 30 days to help the new hire ramp up on team processes.\n",
    ),
]


def _seed_corpus(tmp_path: Path) -> dict[str, int]:
    """Write `_CORPUS_FILES` for real under `tmp_path`, return `{relative_path: file_id}`
    with `file_id`s assigned deterministically (1-based, sorted by relative path)."""
    for relative_path, content in _CORPUS_FILES:
        full_path = tmp_path / relative_path
        full_path.parent.mkdir(parents=True, exist_ok=True)
        full_path.write_text(content)

    sorted_paths = sorted(path for path, _ in _CORPUS_FILES)
    return {path: file_id for file_id, path in enumerate(sorted_paths, start=1)}


def _make_get_file(tmp_path: Path, id_to_path: dict[int, str]):
    """Return a `GetFileFn` that reads the real seeded file off disk on every call
    (genuine I/O against the corpus, not a canned in-memory dict)."""

    def get_file(file_id: int) -> tuple[str, str]:
        path = id_to_path[file_id]
        content = (tmp_path / path).read_text()
        return path, content

    return get_file


def _score(query: str, content: str) -> float:
    """A tiny, genuinely-computed (not hardcoded) keyword-overlap relevance score:
    the fraction of the query's lowercase words that also appear in `content`."""
    query_words = {w.strip(".,?!") for w in query.lower().split()}
    query_words = {w for w in query_words if w}
    if not query_words:
        return 0.0
    content_lower = content.lower()
    hits = sum(1 for w in query_words if w in content_lower)
    return hits / len(query_words)


def _make_search_candidates(tmp_path: Path, path_to_id: dict[str, int]):
    """Return a `SearchCandidatesFn` that re-reads every seeded file's real content off
    disk on each call and scores it against `query` via `_score`, returning
    `TopicCandidate`s sorted descending by score -- a real (if simple) search over the
    real corpus, not a stubbed candidate list."""

    def search_candidates(query: str, max_results: int) -> list[TopicCandidate]:
        candidates = []
        for path, file_id in path_to_id.items():
            content = (tmp_path / path).read_text()
            candidates.append(
                TopicCandidate(file_id=file_id, path=path, score=_score(query, content))
            )
        candidates.sort(key=lambda c: c.score, reverse=True)
        return candidates[:max_results]

    return search_candidates


def _fake_graph_neighbors(file_id: int, hops: int) -> list[GraphNeighbor]:
    """Never expected to be called in this module's tests: the seeded candidates'
    scores are engineered (via `_score`'s real keyword overlap against genuinely
    distinct corpus content) so that no selected topic is ever judged "insufficient
    alone" -- the expansion-call-order path is already covered end-to-end by 4.6.1's
    own `test_pipeline.py`, out of re-scope for this citation-resolution-focused test."""
    raise AssertionError(
        "graph_neighbors should not be called: this test's seeded corpus is scored so "
        "every selected topic is sufficient alone"
    )


class _FakeLLMClient(LLMClient):
    """Fake `LLMClient` (a real ABC subclass, per `llm.client.LLMClient`'s own disclosed
    design) returning canned JSON responses in call order -- one for intent-refinement,
    one for synthesis. Mirrors `test_pipeline.py`'s own `_FakeLLMClient` convention."""

    def __init__(self, responses: list[str]) -> None:
        self._responses = list(responses)
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
        self.calls.append({"prompt": prompt})
        index = len(self.calls) - 1
        assert index < len(self._responses), "more LLM calls than canned responses supplied"
        return self._responses[index]


_INTENT_RESPONSE = json.dumps(
    {
        "refined_intent": "What is the process for disputing a duplicate invoice charge?",
        "entities": ["invoice"],
        "query_type": "factual_lookup",
    }
)


def test_e2e_valid_citation_resolves_to_real_seeded_file(tmp_path: Path) -> None:
    """Seed a real corpus, run a real query through the full pipeline, and assert the
    synthesized answer's citation resolves to a real file that exists in the seeded
    corpus -- no hallucinated citations."""
    path_to_id = _seed_corpus(tmp_path)
    id_to_path = {file_id: path for path, file_id in path_to_id.items()}

    billing_path = "billing/InvoiceDisputes.md"
    assert billing_path in path_to_id  # sanity: the corpus really contains this file

    synthesis_response = json.dumps(
        {
            "answer": (
                "Open a dispute ticket within 30 days of the invoice date "
                f"[{billing_path}]."
            ),
            "citations": [billing_path],
        }
    )
    llm_client = _FakeLLMClient([_INTENT_RESPONSE, synthesis_response])

    result = run_query_pipeline(
        "My customer was charged twice for the same invoice -- how do we dispute it?",
        [],
        llm_client=llm_client,
        search_candidates=_make_search_candidates(tmp_path, path_to_id),
        graph_neighbors=_fake_graph_neighbors,
        get_file=_make_get_file(tmp_path, id_to_path),
        k=1,
        max_candidates=3,
    )

    # The full pipeline (not a mocked step) selected the real seeded billing file.
    assert result.selected_file_ids == [path_to_id[billing_path]]

    # The synthesis prompt embedded a real "## File: <path>" header for the real,
    # on-disk-read seeded file -- i.e. this citation traces back to genuine corpus
    # content, not a hallucination.
    synthesis_prompt = llm_client.calls[1]["prompt"]
    assert f"## File: {billing_path}" in synthesis_prompt
    assert "open a dispute ticket within 30 days" in synthesis_prompt

    # The synthesized answer's citation resolves to a real file that exists in the
    # seeded corpus: it is present in provided_paths (derived from the real
    # get_file-resolved content) and NOT flagged as unknown.
    assert result.synthesis.citations == [billing_path]
    assert billing_path in result.synthesis.provided_paths
    assert result.synthesis.unknown_citations() == []


def test_e2e_hallucinated_citation_is_flagged(tmp_path: Path) -> None:
    """Same real seeded corpus; the fake LLM hallucinates an extra citation to a file
    that is NOT in the seeded corpus. Assert `unknown_citations()` correctly flags the
    hallucinated path while the real, resolvable citation is not flagged."""
    path_to_id = _seed_corpus(tmp_path)
    id_to_path = {file_id: path for path, file_id in path_to_id.items()}

    billing_path = "billing/InvoiceDisputes.md"
    hallucinated_path = "made/up/NotInCorpus.md"
    assert hallucinated_path not in path_to_id  # sanity: genuinely absent from corpus

    synthesis_response = json.dumps(
        {
            "answer": (
                "Open a dispute ticket within 30 days of the invoice date "
                f"[{billing_path}], per our refund policy [{hallucinated_path}]."
            ),
            "citations": [billing_path, hallucinated_path],
        }
    )
    llm_client = _FakeLLMClient([_INTENT_RESPONSE, synthesis_response])

    result = run_query_pipeline(
        "My customer was charged twice for the same invoice -- how do we dispute it?",
        [],
        llm_client=llm_client,
        search_candidates=_make_search_candidates(tmp_path, path_to_id),
        graph_neighbors=_fake_graph_neighbors,
        get_file=_make_get_file(tmp_path, id_to_path),
        k=1,
        max_candidates=3,
    )

    assert result.selected_file_ids == [path_to_id[billing_path]]
    assert result.synthesis.citations == [billing_path, hallucinated_path]

    # The real, seeded, get_file-resolvable citation must NOT be flagged as unknown;
    # the hallucinated one (never present in any "## File:" header built from the real
    # seeded corpus) must be flagged -- full end-to-end citation resolution, not an
    # isolated synthesizer.py unit test.
    assert result.synthesis.unknown_citations() == [hallucinated_path]
    assert billing_path not in result.synthesis.unknown_citations()
