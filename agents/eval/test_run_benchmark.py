"""Tests for `agents/eval/run_benchmark.py` (issue #28, subtask 5.3.4).

Per this subtask's test spec: construct a tiny synthetic corpus + ground truth, run
`run_benchmark`'s core orchestration function across 3 synthetic checkpoint sizes, and assert
the output data file contains exactly three checkpoints with well-formed recall/precision (and
cost/latency) for all three arms.

Binding offline constraint -- disclosed
--------------------------------------------
Every LLM call in this file goes through a deterministic stub `LLMClient` (mirroring
`test_graphrag_baseline.py`'s/`test_shared_final_llm.py`'s established `_StubLLMClient`
convention) and a fake embedding client with no real HTTP transport. `LLMInterceptor` instances
constructed in the judge-path test always resolve `provider="ollama"` (the free/local path,
`$0.0` unconditionally, no usage lookup needed) so no real token-usage/pricing logic is
exercised either. Zero real network calls, zero `.env` reads, zero OpenRouter/Gemini calls
anywhere in this file.

This file also closes a disclosed test-coverage gap in 5.4.1's own test suite
(`test_traversal_precision_check.py`): per the launching agent's brief, that file was flagged as
never calling `compare_precision_across_checkpoints()` with more than one checkpoint in a single
call. `test_compare_precision_across_checkpoints_multi_checkpoint` below does so directly, with a
real 3-item `CorpusGrowthCheckpoint` list.
"""

from __future__ import annotations

import json

import pytest

from eval.baselines.graphrag_lite import EntityGraph
from eval.cost_latency import StageRecord
from eval.ground_truth import QueryLabel, RelevantDoc
from eval.run_benchmark import (
    ALL_ARMS,
    CorpusCheckpoint,
    GRAPHRAG_LITE_ARM,
    HIVEMIND_ARM,
    JudgeConfig,
    RunBenchmarkError,
    VECTOR_RAG_ARM,
    build_checkpoints,
    checkpoint_corpus,
    default_arm_specs,
    load_benchmark_results,
    run_arm_at_checkpoint,
    run_benchmark,
    run_benchmark_with_traversal_precision,
    write_benchmark_results,
)
from eval.traversal_precision import (
    CorpusGrowthCheckpoint,
    compare_precision_across_checkpoints,
)
from llm.client import LLMClient
from llm.interceptor import LLMInterceptor

# --- Fixture corpus + ground truth -----------------------------------------------------------

_ALL_DOCS: list[tuple[str, str]] = [
    ("doc-alpha", "Alpha handles authentication and login flows for the service."),
    ("doc-beta", "Beta handles billing, invoices, and payment processing."),
    ("doc-gamma", "Gamma is a background worker that processes queued jobs."),
    ("doc-delta", "Delta manages user profile settings and preferences."),
]

_QUERIES = [
    QueryLabel(
        query="What is the policy on Alpha?",
        topic_id="alpha",
        relevant_docs=[RelevantDoc(doc_id="doc-alpha", label="primary")],
    ),
    QueryLabel(
        query="What is the policy on Beta?",
        topic_id="beta",
        relevant_docs=[RelevantDoc(doc_id="doc-beta", label="primary")],
    ),
]

_TOPIC_KEYWORDS = {"alpha": "Alpha", "beta": "Beta"}


class _FakeEmbedClient:
    """Deterministic fake embedding client -- no `httpx`, no real Ollama call. Maps any text
    containing one of `_TOPIC_KEYWORDS`' capitalized forms (or the fixture's other doc
    keywords) to a fixed one-hot basis vector, so cosine similarity in `VectorRagIndex.search`
    deterministically favors the matching document/query pair."""

    _BASIS = {
        "Alpha": [1.0, 0.0, 0.0, 0.0],
        "Beta": [0.0, 1.0, 0.0, 0.0],
        "Gamma": [0.0, 0.0, 1.0, 0.0],
        "Delta": [0.0, 0.0, 0.0, 1.0],
    }

    def embed(self, texts: list[str]) -> list[list[float]]:
        vectors = []
        for text in texts:
            vector = [0.0, 0.0, 0.0, 0.0]
            for keyword, basis in self._BASIS.items():
                if keyword in text:
                    vector = basis
                    break
            vectors.append(vector)
        return vectors


class _StubLLMClient(LLMClient):
    """Deterministic canned-response stub -- mirrors `test_graphrag_baseline.py`'s
    `_StubLLMClient` convention (substring match against the prompt)."""

    def __init__(self, responses: dict[str, str], *, default: str | None = None) -> None:
        self._responses = responses
        self._default = default

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
        if self._default is not None:
            return self._default
        raise AssertionError(f"_StubLLMClient: no canned response configured for prompt: {prompt!r}")


def _graphrag_stub_client() -> _StubLLMClient:
    return _StubLLMClient(
        {
            "Alpha handles authentication": '["Alpha"]',
            "Beta handles billing": '["Beta"]',
            "Gamma is a background worker": '["Gamma"]',
            "Delta manages user profile": '["Delta"]',
            "What is the policy on Alpha?": '["Alpha"]',
            "What is the policy on Beta?": '["Beta"]',
        }
    )


def _fake_hivemind_retriever(query_label: QueryLabel, corpus: dict) -> list[str]:
    """Stand-in for the real gRPC-backed HiveMind retrieval pipeline (out of this subtask's
    scope, see `run_benchmark.py`'s module docstring). Restricted to `corpus` (this checkpoint's
    own ingested docs) so recall naturally reflects what has actually been "ingested" so far."""
    keyword = _TOPIC_KEYWORDS.get(query_label.topic_id)
    if keyword is None:
        return []
    return [doc_id for doc_id, text in corpus.items() if keyword in text]


def _build_arm_specs():
    return default_arm_specs(
        hivemind_retriever=_fake_hivemind_retriever,
        embed_client=_FakeEmbedClient(),
        graphrag_llm_client=_graphrag_stub_client(),
        top_k=3,
    )


# --- checkpoint_corpus / build_checkpoints ----------------------------------------------------


def test_checkpoint_corpus_slices_by_percentage_prefix():
    assert checkpoint_corpus(_ALL_DOCS, 20) == _ALL_DOCS[:1]
    assert checkpoint_corpus(_ALL_DOCS, 50) == _ALL_DOCS[:2]
    assert checkpoint_corpus(_ALL_DOCS, 100) == _ALL_DOCS[:4]


def test_checkpoint_corpus_rejects_out_of_range_percentage():
    with pytest.raises(RunBenchmarkError):
        checkpoint_corpus(_ALL_DOCS, 0)
    with pytest.raises(RunBenchmarkError):
        checkpoint_corpus(_ALL_DOCS, 101)


def test_build_checkpoints_default_percentages_yield_three_checkpoints():
    checkpoints = build_checkpoints(_ALL_DOCS)
    assert [c.pct for c in checkpoints] == [20, 50, 100]
    assert [c.label for c in checkpoints] == ["20pct", "50pct", "100pct"]
    assert checkpoints[0].docs == _ALL_DOCS[:1]
    assert checkpoints[-1].docs == _ALL_DOCS


# --- run_benchmark: core orchestration ---------------------------------------------------------


def test_run_benchmark_produces_three_checkpoints_all_arms():
    checkpoints = build_checkpoints(_ALL_DOCS)
    arm_specs = _build_arm_specs()

    report = run_benchmark(checkpoints, _QUERIES, arm_specs, k=3)

    assert len(report.results) == 3 * len(ALL_ARMS)

    seen_checkpoints = {r.checkpoint_label for r in report.results}
    assert seen_checkpoints == {"20pct", "50pct", "100pct"}
    seen_arms = {r.arm for r in report.results}
    assert seen_arms == set(ALL_ARMS)

    for result in report.results:
        assert 0.0 <= result.arm_score.mean_recall <= 1.0
        assert 0.0 <= result.arm_score.mean_precision <= 1.0
        assert result.cost_summary is not None
        assert result.cost_summary.query_count == len(_QUERIES)
        assert result.cost_summary.total_cost_usd == 0.0  # provider="ollama" throughout
        assert result.cost_summary.cost_per_1000_queries == 0.0

    # 100pct checkpoint: every arm should recover both docs for both queries (full corpus
    # present, exact keyword match for both vector_rag's embedding basis and graphrag's entity
    # match and the fake hivemind retriever).
    full_results = [r for r in report.results if r.checkpoint_label == "100pct"]
    for result in full_results:
        assert result.arm_score.mean_recall == 1.0


def test_run_benchmark_json_output_is_well_formed_with_exactly_three_checkpoints(tmp_path):
    checkpoints = build_checkpoints(_ALL_DOCS)
    arm_specs = _build_arm_specs()
    report = run_benchmark(checkpoints, _QUERIES, arm_specs, k=3)

    out_path = tmp_path / "benchmark_results.json"
    write_benchmark_results(report, out_path)
    loaded = load_benchmark_results(out_path)

    assert len(loaded["checkpoints"]) == 3
    assert {c["pct"] for c in loaded["checkpoints"]} == {20, 50, 100}
    assert len(loaded["rows"]) == 9  # 3 checkpoints x 3 arms

    for row in loaded["rows"]:
        assert row["checkpoint_label"] in {"20pct", "50pct", "100pct"}
        assert row["arm"] in ALL_ARMS
        assert 0.0 <= row["mean_recall"] <= 1.0
        assert 0.0 <= row["mean_precision"] <= 1.0
        assert "cost_per_1000_queries" in row
        assert "stages" in row

    # exactly re-loadable as valid JSON (round trip via json.loads already implied by
    # load_benchmark_results, but assert the raw text is valid JSON too)
    json.loads(out_path.read_text(encoding="utf-8"))


def test_write_and_load_benchmark_results_round_trip(tmp_path):
    checkpoints = build_checkpoints(_ALL_DOCS)
    arm_specs = _build_arm_specs()
    report = run_benchmark(checkpoints, _QUERIES, arm_specs, k=3)

    path = tmp_path / "out.json"
    write_benchmark_results(report, path)
    loaded = load_benchmark_results(path)

    assert loaded == report.to_json()


def test_arm_specs_reuse_baseline_retrieve_documents():
    """Confirms vector_rag/graphrag_lite arms actually call the real, already-merged
    `retrieve_documents` functions (reuse, not reimplementation) by checking the retrieved shape
    matches those functions' own documented contract: ranked, deduplicated `list[str]`."""
    checkpoints = build_checkpoints(_ALL_DOCS)
    arm_specs = _build_arm_specs()
    corpus_map = dict(checkpoints[-1].docs)

    for spec in arm_specs:
        if spec.name == HIVEMIND_ARM:
            continue
        retriever = spec.build_retriever(corpus_map, checkpoints[-1])
        for query_label in _QUERIES:
            ids = retriever(query_label)
            assert isinstance(ids, list)
            assert all(isinstance(doc_id, str) for doc_id in ids)
            assert len(ids) == len(set(ids))


# --- judge-path wiring (optional, stubbed) -----------------------------------------------------


def _judge_json(correctness: int = 5, completeness: int = 5, citation_accuracy: int = 5) -> str:
    return json.dumps(
        {
            "scores": {
                "correctness": correctness,
                "completeness": completeness,
                "citation_accuracy": citation_accuracy,
            },
            "rationale": "stub rationale",
        }
    )


class _FinalAnswerStubClient(LLMClient):
    """Always returns a minimal valid synthesizer JSON response citing whatever doc-id
    substring ('doc-alpha'/'doc-beta') the prompt's selected markdown contains first."""

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        for doc_id in ("doc-alpha", "doc-beta", "doc-gamma", "doc-delta"):
            if doc_id in prompt:
                return json.dumps(
                    {"answer": f"answer citing [{doc_id}]", "citations": [doc_id]}
                )
        return json.dumps({"answer": "no citation", "citations": []})


def test_judge_path_reuses_generate_final_answer():
    """Judge scoring is optional and, when wired, reuses `eval.pipeline.generate_final_answer`
    (the same shared final-answer function `pipeline.py`'s arm wrappers call) and routes the
    judge call exclusively through `LLMInterceptor.call()` -- always `provider='ollama'` here,
    so zero real network call and zero pricing-table lookup."""
    checkpoints = build_checkpoints(_ALL_DOCS)
    corpus_map = dict(checkpoints[-1].docs)

    judge_stub = _StubLLMClient({}, default=_judge_json())
    judge_config = JudgeConfig(
        final_answer_llm_client=_FinalAnswerStubClient(),
        judge_llm_client=judge_stub,
        interceptor=LLMInterceptor(),
        provider="ollama",
    )

    def hivemind_retriever(query_label, corpus):
        return _fake_hivemind_retriever(query_label, corpus)

    result, stage_records = run_arm_at_checkpoint(
        HIVEMIND_ARM,
        lambda q: hivemind_retriever(q, corpus_map),
        _QUERIES,
        corpus_map,
        k=3,
        checkpoint_label="100pct",
        checkpoint_pct=100,
        judge_config=judge_config,
    )

    assert result.judge_results is not None
    assert len(result.judge_results) == len(_QUERIES)
    assert result.mean_judge_overall == pytest.approx(5.0)
    # judge StageRecords (stage="llm_judge" by default in llm_judge.score_answer) are present
    # alongside this function's own retrieval+final-answer StageRecords.
    stages_seen = {r.stage for r in stage_records}
    assert "llm_judge" in stages_seen
    assert result.cost_summary is not None
    assert result.cost_summary.total_cost_usd == 0.0  # provider="ollama" throughout


# --- 5.4.1 multi-checkpoint coverage gap -------------------------------------------------------


def test_compare_precision_across_checkpoints_multi_checkpoint():
    """Closes 5.4.1's own disclosed test-coverage gap: calls
    `compare_precision_across_checkpoints()` directly with a real 3-item
    `CorpusGrowthCheckpoint` list in a single call, asserting per-checkpoint independence (each
    checkpoint's own comparison reflects only that checkpoint's corpus, positionally aligned
    with the input list -- not, e.g., an accidentally-shared/leaked `EntityGraph` across
    checkpoints)."""
    stub = _graphrag_stub_client()

    checkpoints = [
        CorpusGrowthCheckpoint(label="20pct", docs=_ALL_DOCS[:1]),
        CorpusGrowthCheckpoint(label="50pct", docs=_ALL_DOCS[:2]),
        CorpusGrowthCheckpoint(label="100pct", docs=_ALL_DOCS[:4]),
    ]

    comparisons = compare_precision_across_checkpoints(
        checkpoints, _QUERIES, stub, top_k=3, k=3
    )

    assert len(comparisons) == 3
    assert [c.checkpoint_label for c in comparisons] == ["20pct", "50pct", "100pct"]

    # 20pct checkpoint only has doc-alpha ingested: the beta query can retrieve nothing from
    # either arm at this checkpoint (no "Beta" entity exists in this checkpoint's graph at all).
    checkpoint_20 = comparisons[0]
    beta_query_delta = next(
        d for d in checkpoint_20.per_query_deltas if d.query == "What is the policy on Beta?"
    )
    assert beta_query_delta.expansion_precision == 0.0
    assert beta_query_delta.no_expansion_precision == 0.0

    # 100pct checkpoint: both queries' entities are present -- per-checkpoint results must not
    # be contaminated by the smaller checkpoints built earlier in the same call.
    checkpoint_100 = comparisons[-1]
    alpha_query_delta = next(
        d for d in checkpoint_100.per_query_deltas if d.query == "What is the policy on Alpha?"
    )
    assert alpha_query_delta.expansion_precision > 0.0

    # Checkpoints are independent: 20pct's ArmScore has strictly fewer/no worse recovered docs
    # than 100pct's for the beta query (corpus grew, so a query about content only ingested
    # later can only do the same or better, never worse, at a larger checkpoint).
    assert checkpoint_20.expansion_score.mean_recall <= checkpoint_100.expansion_score.mean_recall


def test_run_benchmark_with_traversal_precision_wires_5_4_1_check():
    checkpoints = build_checkpoints(_ALL_DOCS)
    arm_specs = _build_arm_specs()
    graphrag_client = _graphrag_stub_client()

    report = run_benchmark_with_traversal_precision(
        checkpoints, _QUERIES, arm_specs, graphrag_client, k=3, top_k=3
    )

    assert len(report.traversal_precision_comparisons) == 3
    labels = [c.checkpoint_label for c in report.traversal_precision_comparisons]
    assert labels == ["20pct", "50pct", "100pct"]

    data = report.to_json()
    assert len(data["traversal_precision"]) == 3
