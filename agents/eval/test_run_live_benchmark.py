"""Tests for `agents/eval/run_live_benchmark.py` (issue #28, subtask 5.3.5).

Per this subtask's acceptance criteria (2): a clearly-scoped subset of this module's logic that
is runnable in CI without real infrastructure or paid APIs -- specifically:

- `ResilientLLMClient`'s retry-until-bare-JSON policy (constraint (c)).
- The `PipelineError` -> `[]` mapping in `LiveHivemindRetriever.__call__` (constraint (b)).
- The path-casing choice in `load_live_corpus` (constraint (a)): the topic title is passed
  through unmodified, in its original casing.
- `CostCappedInterceptor`'s fail-closed cap enforcement (module docstring, point 8).
- The cost-accounting integrity constraint (d): the real paid judge client is never wrapped in
  `ResilientLLMClient`.

Zero real infrastructure, zero real network calls, zero `.env` reads anywhere in this file --
`smokeserver`/gRPC/Ollama/OpenRouter/Gemini are all mocked or entirely avoided.
"""

from __future__ import annotations

import json

import pytest

from eval.cost_latency import StageRecord
from eval.ground_truth import QueryLabel, RelevantDoc
from eval.run_live_benchmark import (
    CostCapExceededError,
    CostCappedInterceptor,
    LiveHivemindRetriever,
    ResilientLLMClient,
    _build_judge_config,
    _looks_like_bare_json,
    load_live_corpus,
    sum_real_cost_usd,
)
from llm.client import LLMClient

# --- _looks_like_bare_json ----------------------------------------------------------------


def test_looks_like_bare_json_true_for_valid_json():
    assert _looks_like_bare_json('{"a": 1}') is True
    assert _looks_like_bare_json('  {"a": 1}  \n') is True


def test_looks_like_bare_json_false_for_markdown_fenced():
    assert _looks_like_bare_json('```json\n{"a": 1}\n```') is False


def test_looks_like_bare_json_false_for_prose_prefixed():
    assert _looks_like_bare_json('Here is the answer: {"a": 1}') is False


def test_looks_like_bare_json_false_for_empty_string():
    assert _looks_like_bare_json("") is False


# --- ResilientLLMClient --------------------------------------------------------------------


class _ScriptedLLMClient(LLMClient):
    """Returns each entry of `responses` in order, one per `complete()` call; records every
    call's `temperature` for assertion."""

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
        self.calls.append({"temperature": temperature})
        return self._responses[len(self.calls) - 1]


def test_resilient_client_returns_first_valid_json_attempt():
    inner = _ScriptedLLMClient(["not json", '{"ok": true}', "unused"])
    client = ResilientLLMClient(inner)

    result = client.complete("prompt", temperature=0.0)

    assert result == '{"ok": true}'
    assert len(inner.calls) == 2
    # First attempt uses the caller's requested temperature, retries bump it.
    assert inner.calls[0]["temperature"] == 0.0
    assert inner.calls[1]["temperature"] == pytest.approx(0.3)


def test_resilient_client_returns_last_attempt_if_never_valid():
    inner = _ScriptedLLMClient(
        ["not json", "still not json", "nope either", "nope again", "final nope"]
    )
    client = ResilientLLMClient(inner)

    result = client.complete("prompt")

    assert result == "final nope"
    assert len(inner.calls) == 5


def test_resilient_client_single_valid_first_attempt_no_retry():
    inner = _ScriptedLLMClient(['{"ok": true}'])
    client = ResilientLLMClient(inner)

    result = client.complete("prompt")

    assert result == '{"ok": true}'
    assert len(inner.calls) == 1


# --- LiveHivemindRetriever: PipelineError -> [] mapping (constraint (b)) -------------------


class _RaisingPipeline:
    """Stand-in for `query.pipeline.run_query_pipeline` that always raises `PipelineError`,
    simulating a cold-miss (zero candidates surfaced)."""

    def __call__(self, *args, **kwargs):
        from query.pipeline import PipelineError

        raise PipelineError("no candidates")


def test_retriever_maps_pipeline_error_to_empty_list(monkeypatch):
    retriever = LiveHivemindRetriever.__new__(LiveHivemindRetriever)
    # Bypass __init__ (which would build real paths/dirs) -- only what __call__ touches is set.
    retriever._doc_titles = {"doc-a": "Topic A"}
    retriever._retrieval_llm_client = object()
    retriever._current_signature = frozenset({"doc-a"})
    retriever._doc_id_by_file_id = {1: "doc-a"}
    retriever._search_candidates = object()
    retriever._graph_neighbors = object()
    retriever._get_file = object()

    monkeypatch.setattr("query.pipeline.run_query_pipeline", _RaisingPipeline())

    query_label = QueryLabel(
        query="What is the policy on Topic A?",
        topic_id="topic-a",
        relevant_docs=[RelevantDoc(doc_id="doc-a", label="primary")],
    )
    result = retriever(query_label, {"doc-a": "text"})

    assert result == []


def test_retriever_maps_selected_file_ids_back_to_doc_ids(monkeypatch):
    retriever = LiveHivemindRetriever.__new__(LiveHivemindRetriever)
    retriever._doc_titles = {"doc-a": "Topic A", "doc-b": "Topic B"}
    retriever._retrieval_llm_client = object()
    retriever._current_signature = frozenset({"doc-a", "doc-b"})
    retriever._doc_id_by_file_id = {1: "doc-a", 2: "doc-b"}
    retriever._search_candidates = object()
    retriever._graph_neighbors = object()
    retriever._get_file = object()

    class _FakeResult:
        selected_file_ids = [2, 1, 999]  # 999 has no known doc_id -- must be skipped, not raise.

    monkeypatch.setattr(
        "query.pipeline.run_query_pipeline", lambda *a, **k: _FakeResult()
    )

    query_label = QueryLabel(
        query="What is the policy on Topic A?", topic_id="topic-a", relevant_docs=[]
    )
    result = retriever(query_label, {"doc-a": "text", "doc-b": "text"})

    assert result == ["doc-b", "doc-a"]


# --- load_live_corpus: path-casing choice (constraint (a)) ---------------------------------


def test_load_live_corpus_preserves_original_title_casing(monkeypatch, tmp_path):
    manifest = {
        "documents": [
            {
                "doc_id": "doc-data-retention",
                "filename": "doc-data-retention.pdf",
                "primary_topic": {"id": "data-retention", "title": "Data Retention Policy"},
                "cross_references": [],
            }
        ]
    }
    manifest_path = tmp_path / "manifest.json"
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

    monkeypatch.setattr(
        "ingestion.normalize_pdf.normalize_pdf",
        lambda path: "normalized text",
    )

    all_docs, doc_titles = load_live_corpus(manifest_path=manifest_path, corpus_dir=tmp_path)

    assert all_docs == [("doc-data-retention", "normalized text")]
    # Original casing, verbatim -- never lowercased/slugified (constraint (a)).
    assert doc_titles["doc-data-retention"] == "Data Retention Policy"
    assert doc_titles["doc-data-retention"] != "Data Retention Policy".lower()


# --- CostCappedInterceptor: fail-closed cap enforcement -------------------------------------


class _CountingJudgeClient(LLMClient):
    def __init__(self) -> None:
        self.call_count = 0

    def complete(self, prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None):
        self.call_count += 1
        return '{"relevance": 5, "correctness": 5, "completeness": 5, "overall": 5}'


def test_cost_capped_interceptor_blocks_once_cap_reached():
    interceptor = CostCappedInterceptor(cap_usd=0.0)
    client = _CountingJudgeClient()

    with pytest.raises(CostCapExceededError):
        interceptor.call(
            client,
            provider="openrouter",
            arm="hivemind",
            stage="llm_judge",
            prompt="judge this",
        )

    # Fail-closed: the call must never actually have been attempted once the cap was reached.
    assert client.call_count == 0


def test_cost_capped_interceptor_allows_free_provider_regardless_of_cap():
    interceptor = CostCappedInterceptor(cap_usd=0.0)
    client = _CountingJudgeClient()

    intercepted = interceptor.call(
        client, provider="ollama", arm="hivemind", stage="llm_judge", prompt="judge this"
    )

    assert client.call_count == 1
    assert intercepted.record.cost_usd == 0.0


# --- sum_real_cost_usd -----------------------------------------------------------------------


def test_sum_real_cost_usd_sums_resolved_costs():
    records = [
        StageRecord(arm="a", stage="s", duration_seconds=0.1, provider="ollama"),
        StageRecord(
            arm="a", stage="s", duration_seconds=0.1, provider="openrouter", cost_usd=0.002
        ),
    ]
    assert sum_real_cost_usd(records) == pytest.approx(0.002)


# --- Cost-accounting integrity constraint (d): judge client never wrapped ------------------


@pytest.mark.parametrize("provider", ["ollama", "openrouter", "gemini"])
def test_judge_client_never_wrapped_in_resilient_client(monkeypatch, provider):
    """For every supported `--judge-provider` value, `_build_judge_config`'s `judge_llm_client`
    must never be a `ResilientLLMClient` -- see module docstring's constraint (d)."""

    class _FakeJudgeClient(LLMClient):
        def complete(self, prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None):
            return '{"relevance": 5, "correctness": 5, "completeness": 5, "overall": 5}'

    fake_client = _FakeJudgeClient()
    monkeypatch.setattr(
        "eval.run_live_benchmark.create_llm_client", lambda provider, **kwargs: fake_client
    )

    judge_config, interceptor = _build_judge_config(
        judge_provider=provider,
        judge_model=None,
        cost_cap_usd=1.0,
        final_answer_llm_client=ResilientLLMClient(fake_client),
    )

    assert judge_config.judge_llm_client is fake_client
    assert not isinstance(judge_config.judge_llm_client, ResilientLLMClient)
    assert isinstance(interceptor, CostCappedInterceptor)
