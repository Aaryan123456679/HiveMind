"""Tests for `agents/eval/llm_judge.py` and `agents/eval/calibrate_judge.py` (issue #28,
subtask 5.3.2).

Per subtask 5.3.2's test spec: "pytest agents/eval/test_llm_judge.py (LLM-judge call mocked):
assert scoring pipeline produces expected score structure; a manual calibration script is
runnable and produces a comparison report." Both modules' impacted-file list names only this
one test file, so it covers both.

No live LLM calls anywhere in this file -- binding scoping constraint
--------------------------------------------------------------------------
Every test uses either a hand-written `_StubLLMClient(LLMClient)` (mirroring
`agents/eval/test_shared_final_llm.py`'s `_SpyLLMClient` convention) or an
`httpx.MockTransport`-backed `OllamaClient` (mirroring `agents/llm/test_interceptor.py`'s
convention). No real network call, no `.env` read, no OpenRouter/Gemini API spend anywhere in
this suite.
"""

from __future__ import annotations

import json

import httpx
import pytest

from eval.calibrate_judge import (
    CalibrationReport,
    ManualRating,
    load_manual_ratings,
    main as calibrate_main,
    run_calibration,
    write_report,
)
from eval.cost_latency import StageRecord, aggregate_by_stage
from eval.llm_judge import (
    JUDGE_RUBRIC_CRITERIA,
    JudgeError,
    JudgeScore,
    build_judge_prompt,
    parse_judge_response,
    score_answer,
    score_arm_answers,
)
from llm.client import LLMClient
from llm.interceptor import LLMInterceptor
from llm.ollama_client import OllamaClient


def _judge_json(correctness: int = 5, completeness: int = 4, citation_accuracy: int = 3, rationale: str = "ok") -> str:
    return json.dumps(
        {
            "scores": {
                "correctness": correctness,
                "completeness": completeness,
                "citation_accuracy": citation_accuracy,
            },
            "rationale": rationale,
        }
    )


class _StubLLMClient(LLMClient):
    """Records every `complete()`/`complete_with_usage()` call; returns a canned judge-JSON
    response. Mirrors `test_shared_final_llm.py`'s `_SpyLLMClient` convention."""

    def __init__(self, response_text: str) -> None:
        self.response_text = response_text
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
        return self.response_text


# ---------------------------------------------------------------------------
# build_judge_prompt
# ---------------------------------------------------------------------------


def test_build_judge_prompt_includes_rubric_criteria() -> None:
    prompt = build_judge_prompt("What is the refund policy?", "You get a refund within 30 days.")
    for criterion in JUDGE_RUBRIC_CRITERIA:
        assert criterion in prompt
    assert "What is the refund policy?" in prompt
    assert "You get a refund within 30 days." in prompt


def test_build_judge_prompt_includes_reference_context_when_given() -> None:
    prompt = build_judge_prompt("q", "a", reference_context="## File: doc-a\n\nsome context")
    assert "some context" in prompt


def test_build_judge_prompt_omits_context_section_when_empty() -> None:
    prompt = build_judge_prompt("q", "a")
    assert "Reference context" not in prompt


# ---------------------------------------------------------------------------
# parse_judge_response
# ---------------------------------------------------------------------------


def test_parse_judge_response_valid() -> None:
    score = parse_judge_response("q", "a", _judge_json(correctness=5, completeness=4, citation_accuracy=3))
    assert isinstance(score, JudgeScore)
    assert score.query == "q"
    assert score.answer == "a"
    assert score.scores == {"correctness": 5, "completeness": 4, "citation_accuracy": 3}
    assert score.overall == pytest.approx(4.0)
    assert score.rationale == "ok"


def test_parse_judge_response_missing_criterion_raises() -> None:
    raw = json.dumps({"scores": {"correctness": 5, "completeness": 4}, "rationale": "x"})
    with pytest.raises(JudgeError):
        parse_judge_response("q", "a", raw)


def test_parse_judge_response_out_of_range_score_raises() -> None:
    with pytest.raises(JudgeError):
        parse_judge_response("q", "a", _judge_json(correctness=6))


def test_parse_judge_response_non_int_score_raises() -> None:
    raw = json.dumps(
        {"scores": {"correctness": "5", "completeness": 4, "citation_accuracy": 3}, "rationale": "x"}
    )
    with pytest.raises(JudgeError):
        parse_judge_response("q", "a", raw)


def test_parse_judge_response_invalid_json_raises() -> None:
    with pytest.raises(JudgeError):
        parse_judge_response("q", "a", "not json at all {")


def test_parse_judge_response_non_object_json_raises() -> None:
    with pytest.raises(JudgeError):
        parse_judge_response("q", "a", json.dumps([1, 2, 3]))


def test_parse_judge_response_missing_scores_key_raises() -> None:
    with pytest.raises(JudgeError):
        parse_judge_response("q", "a", json.dumps({"rationale": "x"}))


def test_parse_judge_response_to_json_round_trip() -> None:
    score = parse_judge_response("q", "a", _judge_json())
    as_json = score.to_json()
    assert as_json["query"] == "q"
    assert as_json["overall"] == score.overall
    assert as_json["scores"] == dict(score.scores)


# ---------------------------------------------------------------------------
# score_answer -- expected score structure, mocked LLM client
# ---------------------------------------------------------------------------


def test_score_answer_returns_expected_score_structure() -> None:
    client = _StubLLMClient(_judge_json(correctness=5, completeness=5, citation_accuracy=5))
    interceptor = LLMInterceptor()

    result = score_answer(
        "What is the refund policy?",
        "You get a refund within 30 days.",
        client,
        interceptor,
        arm="hivemind",
        provider="ollama",
    )

    assert isinstance(result.score, JudgeScore)
    assert result.score.overall == pytest.approx(5.0)
    assert result.score.scores == {"correctness": 5, "completeness": 5, "citation_accuracy": 5}
    assert isinstance(result.record, StageRecord)
    assert result.record.arm == "hivemind"
    assert result.record.stage == "llm_judge"
    assert result.record.provider == "ollama"
    assert result.record.cost_usd == 0.0


def test_score_answer_record_is_aggregate_by_stage_compatible() -> None:
    client = _StubLLMClient(_judge_json())
    interceptor = LLMInterceptor()
    result = score_answer("q", "a", client, interceptor, arm="vector_rag", query_id="q1")

    aggregates = aggregate_by_stage([result.record])
    assert len(aggregates) == 1
    assert aggregates[0].arm == "vector_rag"
    assert aggregates[0].stage == "llm_judge"
    assert aggregates[0].call_count == 1


def test_score_answer_raises_on_malformed_judge_response() -> None:
    client = _StubLLMClient("not json")
    interceptor = LLMInterceptor()
    with pytest.raises(JudgeError):
        score_answer("q", "a", client, interceptor, arm="hivemind")


# ---------------------------------------------------------------------------
# score_answer -- interceptor integration proof, httpx.MockTransport (mirrors
# agents/llm/test_interceptor.py's own convention)
# ---------------------------------------------------------------------------


def test_score_answer_via_intercepted_ollama_mock_transport() -> None:
    judge_text = _judge_json(correctness=4, completeness=4, citation_accuracy=4)

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"response": judge_text})

    client = OllamaClient(transport=httpx.MockTransport(handler))
    interceptor = LLMInterceptor()

    result = score_answer("q", "a", client, interceptor, arm="hivemind", provider="ollama")

    assert result.score.overall == pytest.approx(4.0)
    assert result.record.provider == "ollama"
    assert result.record.cost_usd == 0.0
    assert result.record.duration_seconds >= 0.0


# ---------------------------------------------------------------------------
# score_arm_answers
# ---------------------------------------------------------------------------


def test_score_arm_answers_scores_every_query() -> None:
    client = _StubLLMClient(_judge_json())
    interceptor = LLMInterceptor()
    answers = {
        "What is the refund policy?": "Refunds within 30 days.",
        "What is the shipping policy?": "Ships within 5 business days.",
    }

    results = score_arm_answers("vector_rag", answers, client, interceptor)

    assert len(results) == 2
    assert [r.score.query for r in results] == list(answers.keys())
    assert all(r.record.arm == "vector_rag" for r in results)
    assert all(r.record.query_id == q for r, q in zip(results, answers.keys()))


# ---------------------------------------------------------------------------
# calibrate_judge.run_calibration -- hand-verified fixture (mirrors
# test_cost_latency_aggregation.py's "literal fixture, == assertion" convention)
# ---------------------------------------------------------------------------


def test_run_calibration_report_structure_hand_verified() -> None:
    # Judge always returns overall == 4.0 (4,4,4 -> mean 4.0).
    client = _StubLLMClient(_judge_json(correctness=4, completeness=4, citation_accuracy=4))
    interceptor = LLMInterceptor()

    ratings = [
        ManualRating(query="q1", answer="a1", human_score=4.0),  # delta 0.0
        ManualRating(query="q2", answer="a2", human_score=3.0),  # delta 1.0
        ManualRating(query="q3", answer="a3", human_score=1.5),  # delta 2.5
    ]

    report = run_calibration(ratings, client, interceptor)

    assert isinstance(report, CalibrationReport)
    assert report.n == 3
    deltas = [round(s.delta, 4) for s in report.samples]
    assert deltas == [0.0, 1.0, 2.5]
    assert report.mean_absolute_delta == pytest.approx((0.0 + 1.0 + 2.5) / 3)
    assert report.max_absolute_delta == pytest.approx(2.5)
    # Within-1-point agreement: samples 1 and 2 (deltas 0.0, 1.0) agree; sample 3 (2.5) doesn't.
    assert report.agreement_within_1_point == pytest.approx(2 / 3)


def test_run_calibration_empty_ratings_yields_vacuous_report() -> None:
    client = _StubLLMClient(_judge_json())
    interceptor = LLMInterceptor()
    report = run_calibration([], client, interceptor)
    assert report.n == 0
    assert report.samples == []
    assert report.mean_absolute_delta == 0.0
    assert report.agreement_within_1_point == 1.0


# ---------------------------------------------------------------------------
# load_manual_ratings / write_report round-trip
# ---------------------------------------------------------------------------


def test_load_manual_ratings_and_write_report_round_trip(tmp_path) -> None:
    ratings_path = tmp_path / "ratings.json"
    ratings_path.write_text(
        json.dumps(
            [
                {"query": "q1", "answer": "a1", "human_score": 4.0, "human_rationale": "good"},
                {"query": "q2", "answer": "a2", "human_score": 2.0},
            ]
        )
    )

    ratings = load_manual_ratings(ratings_path)
    assert len(ratings) == 2
    assert ratings[0] == ManualRating(query="q1", answer="a1", human_score=4.0, human_rationale="good")
    assert ratings[1].human_rationale == ""

    client = _StubLLMClient(_judge_json())
    interceptor = LLMInterceptor()
    report = run_calibration(ratings, client, interceptor)

    out_path = tmp_path / "nested" / "report.json"
    write_report(report, out_path)
    assert out_path.exists()
    written = json.loads(out_path.read_text())
    assert written["n"] == 2
    assert len(written["samples"]) == 2


def test_load_manual_ratings_missing_file_raises(tmp_path) -> None:
    with pytest.raises(FileNotFoundError):
        load_manual_ratings(tmp_path / "does_not_exist.json")


def test_load_manual_ratings_malformed_json_raises(tmp_path) -> None:
    bad_path = tmp_path / "bad.json"
    bad_path.write_text("not json")
    with pytest.raises(ValueError):
        load_manual_ratings(bad_path)


def test_load_manual_ratings_missing_required_field_raises(tmp_path) -> None:
    bad_path = tmp_path / "bad.json"
    bad_path.write_text(json.dumps([{"query": "q1", "answer": "a1"}]))
    with pytest.raises(ValueError):
        load_manual_ratings(bad_path)


# ---------------------------------------------------------------------------
# CLI smoke test -- runnable script producing a comparison report, no real provider
# ---------------------------------------------------------------------------


def test_calibrate_judge_main_cli_smoke_produces_report(tmp_path, monkeypatch: pytest.MonkeyPatch) -> None:
    ratings_path = tmp_path / "ratings.json"
    ratings_path.write_text(
        json.dumps(
            [
                {"query": "q1", "answer": "a1", "human_score": 4.0},
                {"query": "q2", "answer": "a2", "human_score": 3.0},
            ]
        )
    )
    out_path = tmp_path / "report.json"

    stub_client = _StubLLMClient(_judge_json(correctness=4, completeness=4, citation_accuracy=4))
    monkeypatch.setattr(
        "llm.factory.create_llm_client", lambda provider, **kwargs: stub_client
    )

    calibrate_main(["--ratings", str(ratings_path), "--out", str(out_path), "--provider", "ollama"])

    assert out_path.exists()
    written = json.loads(out_path.read_text())
    assert written["n"] == 2
    assert len(written["samples"]) == 2
    assert "mean_absolute_delta" in written
    assert "agreement_within_1_point" in written
    # Prove the judge model was actually invoked (not a no-op script) and no real network call
    # could have been made -- the stub client recorded both calls itself.
    assert len(stub_client.calls) == 2
