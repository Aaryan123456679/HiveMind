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
`LLMClient` -- a disclosed, accepted DI seam. Issue #56 subtask 4.6.3.1 has since added a
real `wiring.py` analogue (`agents/query/wiring.py`, mirroring `agents/ingestion/`'s own),
but this module still injects plain on-disk fakes rather than `wiring.py`'s gRPC-backed
clients: standing up a real running engine process for this test to dial is out of scope
here (contrast `agents/ingestion/test_e2e_smoke.py`, which does exactly that for the
ingestion side) -- that remains future work (see this run's handoff.json). "End-to-end" in
this module means: every non-LLM, non-gRPC step of the real pipeline (`select_top_k`,
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
    """Return a `GetFileFn` that reads the real seeded file's content off disk on every call
    (genuine I/O against the corpus, not a canned in-memory dict). `file_id -> (path,
    content)`, matching `GetFileFn`'s real (post-4.6.3.2) shape -- see `pipeline.py`'s
    "Residual gap -- closed by issue #56 subtask 4.6.3.2" docstring section."""

    def get_file(file_id: int) -> tuple[str, str]:
        path = id_to_path[file_id]
        return path, (tmp_path / path).read_text()

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


# ---------------------------------------------------------------------------
# k=2 topic-expansion test (issue #56 subtask 4.6.3.3, closes finding F-4.6.2-1)
# ---------------------------------------------------------------------------
#
# The two tests above use k=1, which structurally guarantees `is_insufficient_alone`
# never flags the single selected topic (its own score is always >= ratio * itself for any
# ratio <= 1), so `expand_insufficient_topics` never calls `graph_neighbors` -- confirmed by
# `_fake_graph_neighbors` above raising `AssertionError` if it is ever invoked. That leaves
# the topic-expansion (graph-traversal) branch entirely uncovered against a real, on-disk
# seeded corpus with a citation-resolution assertion (flagged as F-4.6.2-1 during issue
# #25 subtask 4.6.2's verification, `.cdr/index/regression.jsonl`).
#
# This test uses a separate, local corpus (`_K2_CORPUS_FILES` / `_seed_k2_corpus`) rather
# than the module-level `_CORPUS_FILES` above, so the existing k=1 tests' fixtures and file
# IDs are left completely undisturbed. It reuses `_make_search_candidates`, `_make_get_file`,
# `_score`, and `_FakeLLMClient` unmodified -- all are already generic over whatever
# `tmp_path`/`path_to_id`/`id_to_path` a caller seeds, not hardwired to `_CORPUS_FILES`.

#: A second, independent real corpus, engineered (via genuinely computed `_score` overlap
#: against the query below, not a hardcoded score) so that with `k=2`:
#:   - "billing/InvoiceDisputes.md" scores highest (topic A, selected, sufficient alone).
#:   - "billing/RefundPolicy.md" scores second (topic B, selected, but < 0.5 * A's score --
#:     genuinely flagged "insufficient alone" by `is_insufficient_alone`'s real, unmodified
#:     ratio check).
#:   - "billing/ChargebackProcess.md" scores zero for this query (real content, but no
#:     keyword overlap) -- never selected via `search_candidates`'s own top-k, reachable in
#:     the final result *only* via the fake `graph_neighbors`' returned neighbor below.
#:   - The remaining two files are zero-overlap distractors, reused verbatim from the k=1
#:     corpus above, confirming unrelated topics are never selected or expanded.
_K2_CORPUS_FILES: list[tuple[str, str]] = [
    (
        "billing/ChargebackProcess.md",
        "# Chargeback Process\n\n"
        "When a customer initiates a chargeback with their card issuer instead of "
        "contacting support directly, the bank notifies our payments team, who compiles "
        "supporting transaction documentation and responds within the issuer's deadline.\n",
    ),
    (
        "billing/InvoiceDisputes.md",
        "# Invoice Disputes\n\n"
        "If a customer disputes an invoice or believes they were charged twice for the "
        "same order, open a dispute ticket within 30 days of the invoice date. Duplicate "
        "charges are refunded automatically once the dispute is confirmed by billing.\n",
    ),
    (
        "billing/RefundPolicy.md",
        "# Refund Exception Policy\n\n"
        "Some refund requests fall outside the standard approval workflow due to "
        "exceptional circumstances. When a refund exception applies, finance manually "
        "reviews the transaction before approving it, separately from routine refunds.\n",
    ),
    (
        "onboarding/NewHireChecklist.md",
        "# New Hire Onboarding Checklist\n\n"
        "New employees complete IT provisioning, benefits enrollment, and security "
        "training within their first week. Managers assign a buddy for the first 30 days "
        "to help the new hire ramp up on team processes.\n",
    ),
    (
        "security/ApiKeyRotation.md",
        "# API Key Rotation Policy\n\n"
        "All API keys issued to external integrations must be rotated every 90 days. "
        "Rotation is performed by generating a new key, updating the integration's "
        "configured secret, and revoking the old key after a 24-hour grace period.\n",
    ),
]

_K2_QUERY = "How should we handle an invoice dispute over a duplicate charge, and are there refund exceptions?"

#: `run_query_pipeline` calls `search_candidates(intent.refined_intent, ...)`, not the raw
#: query -- so the fake LLM's canned intent-refinement response, not `_K2_QUERY` itself, is
#: what `_score` actually scores candidates against. Deliberately set to exactly the four
#: keywords the corpus above is engineered around, so the scoring math documented in
#: `plan.md` holds regardless of `_K2_QUERY`'s own (more naturalistic) wording.
_K2_INTENT_RESPONSE = json.dumps(
    {
        "refined_intent": "invoice dispute duplicate refund",
        "entities": ["invoice"],
        "query_type": "factual_lookup",
    }
)


def _seed_k2_corpus(tmp_path: Path) -> dict[str, int]:
    """Write `_K2_CORPUS_FILES` for real under `tmp_path`, return `{relative_path: file_id}`
    with `file_id`s assigned deterministically (1-based, sorted by relative path). Mirrors
    `_seed_corpus` exactly but operates on the separate `_K2_CORPUS_FILES` list, so this
    test's corpus and IDs never collide with or perturb the k=1 tests above."""
    for relative_path, content in _K2_CORPUS_FILES:
        full_path = tmp_path / relative_path
        full_path.parent.mkdir(parents=True, exist_ok=True)
        full_path.write_text(content)

    sorted_paths = sorted(path for path, _ in _K2_CORPUS_FILES)
    return {path: file_id for file_id, path in enumerate(sorted_paths, start=1)}


class _RecordingGraphNeighbors:
    """Real `GraphNeighborsFn` fake that *records* every call it receives (instead of
    unconditionally raising, like `_fake_graph_neighbors` above) and asserts it is only ever
    called for the one topic this test expects to be flagged "insufficient alone" -- so an
    accidental extra expansion call (e.g. for the topic that should be judged sufficient)
    fails the test loudly rather than silently returning plausible-looking data.

    Returns a single real on-disk corpus file (`billing/ChargebackProcess.md`) as the
    neighbor, proving the topic-expansion branch's output genuinely reaches
    `combine_and_cap`'s final `file_id` list, not just an in-memory topic_selector unit
    assertion (already covered by `test_pipeline.py`/`test_topic_selector_expansion.py`).
    """

    def __init__(self, expected_file_id: int, neighbor_file_id: int) -> None:
        self._expected_file_id = expected_file_id
        self._neighbor_file_id = neighbor_file_id
        self.calls: list[tuple[int, int]] = []

    def __call__(self, file_id: int, hops: int) -> list[GraphNeighbor]:
        self.calls.append((file_id, hops))
        assert file_id == self._expected_file_id, (
            f"graph_neighbors called for unexpected file_id={file_id} (expected "
            f"{self._expected_file_id}); is_insufficient_alone should only flag the one "
            "genuinely low-scoring topic, not the top-scoring one"
        )
        return [
            GraphNeighbor(
                file_id=self._neighbor_file_id,
                edge_type="related_to",
                weight=5,
                hop=1,
            )
        ]


def test_e2e_k2_multi_hop_topic_expansion(tmp_path: Path) -> None:
    """`k=2` end-to-end test: two real topics selected via `select_top_k`, one of them
    genuinely flagged "insufficient alone" by the real, unmodified `is_insufficient_alone`
    ratio check, triggering a real `expand_insufficient_topics` -> `graph_neighbors` call
    whose returned neighbor (a real, on-disk, seeded-corpus file reachable *only* via graph
    expansion) is folded by the real `combine_and_cap` into the final selection and
    correctly resolved, path and all, by the full pipeline's citation-resolution logic.

    Closes issue #56 subtask 4.6.3.3 / finding F-4.6.2-1: the k=1 tests above structurally
    never exercise this branch (`_fake_graph_neighbors` raises if ever called), and
    `test_pipeline.py`'s own k=2 expansion coverage uses a hardcoded in-memory fixture with
    no citation-resolution assertion -- neither combination existed before this test.
    """
    path_to_id = _seed_k2_corpus(tmp_path)
    id_to_path = {file_id: path for path, file_id in path_to_id.items()}

    invoice_path = "billing/InvoiceDisputes.md"
    refund_path = "billing/RefundPolicy.md"
    chargeback_path = "billing/ChargebackProcess.md"
    invoice_id = path_to_id[invoice_path]
    refund_id = path_to_id[refund_path]
    chargeback_id = path_to_id[chargeback_path]

    graph_neighbors = _RecordingGraphNeighbors(
        expected_file_id=refund_id, neighbor_file_id=chargeback_id
    )

    synthesis_response = json.dumps(
        {
            "answer": (
                f"Open a dispute ticket within 30 days of the invoice date [{invoice_path}]. "
                f"Refund exceptions follow a separate manual-review process "
                f"[{refund_path}]. If the customer instead files a chargeback with their "
                f"card issuer, our payments team responds within the issuer's deadline "
                f"[{chargeback_path}]."
            ),
            "citations": [invoice_path, refund_path, chargeback_path],
        }
    )
    llm_client = _FakeLLMClient([_K2_INTENT_RESPONSE, synthesis_response])

    result = run_query_pipeline(
        _K2_QUERY,
        [],
        llm_client=llm_client,
        search_candidates=_make_search_candidates(tmp_path, path_to_id),
        graph_neighbors=graph_neighbors,
        get_file=_make_get_file(tmp_path, id_to_path),
        k=2,
        max_candidates=5,
    )

    # Real multi-candidate selection (k=2, not k=1): both topics selected, in descending-
    # score order, plus the expansion-only neighbor folded in by combine_and_cap -- proves
    # the graph-traversal branch's output genuinely reaches the final file_id list.
    assert result.selected_file_ids == [invoice_id, refund_id, chargeback_id]

    # Real, per-topic insufficiency decision: graph_neighbors was called exactly once, for
    # the genuinely low-scoring topic (RefundPolicy) at the real default hop depth (2) --
    # not for the top-scoring topic (InvoiceDisputes), and not zero or multiple times.
    assert graph_neighbors.calls == [(refund_id, 2)]

    # The synthesis prompt must carry all three real "## File:" headers, including the
    # graph-expansion-only file -- proving `get_file`'s own returned path (not `path_by_id`,
    # which has no entry for a file_id reachable only via GraphNeighbors) was correctly used
    # as the fallback path source (the residual gap closed by issue #56 subtask 4.6.3.2,
    # now exercised for real end-to-end, on-disk, not just in-memory).
    synthesis_prompt = llm_client.calls[1]["prompt"]
    assert f"## File: {invoice_path}" in synthesis_prompt
    assert f"## File: {refund_path}" in synthesis_prompt
    assert f"## File: {chargeback_path}" in synthesis_prompt
    assert "chargeback" in synthesis_prompt.lower()

    # Full end-to-end citation resolution across the multi-hop, multi-candidate result: all
    # three real, seeded, get_file-resolvable citations must resolve -- no hallucinations.
    assert result.synthesis.citations == [invoice_path, refund_path, chargeback_path]
    assert result.synthesis.unknown_citations() == []
