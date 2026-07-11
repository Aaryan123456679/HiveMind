"""LLM-judge answer-quality scoring on a defined rubric (issue #28, subtask 5.3.2).

Per subtask 5.3.2's acceptance criteria: "An LLM-judge scoring pass rates answer quality on a
defined rubric, with a manual spot-check harness allowing a human to compare judge scores
against a sample of manually-rated answers for calibration." `docs/LLD/eval.md` names
"LLM-judge answer quality + manual spot-check calibration" as one of the benchmark's headline
metrics, alongside recall/precision@k (`metrics.py`, 5.3.1) and per-stage latency/cost
(`cost_latency.py`, 5.3.3) -- this module covers the LLM-judge half. The manual calibration
harness itself lives in the sibling module `agents/eval/calibrate_judge.py`.

Defined rubric -- disclosed design
-----------------------------------
The LLD names "LLM-judge answer quality" but does not itself define a rubric (same
LLD-silent-on-detail pattern as 5.3.1's `include_cross_reference` simplification and 5.3.3's
"no pricing table" disclosure, both resolved as in-module disclosed choices rather than a
doc edit). `JUDGE_RUBRIC_CRITERIA` below defines three criteria, each scored on a 1-5 integer
scale: `correctness` (is the answer factually consistent with the retrieved context),
`completeness` (does it address the full query, not just part of it), and
`citation_accuracy` (do its inline file-path citations -- per
`query.synthesizer.synthesize_answer`'s own "answer with inline file-path citations" contract,
see `agents/eval/pipeline.py` -- actually point at supporting content). These three map
directly onto what this benchmark's other two arms already need judged uniformly: whether an
answer is right, whether it is complete, and whether its citations can be trusted -- the same
three axes `docs/LLD/eval.md`'s "Known risks" section implicitly cares about (graph-expansion
context blow-up hurting precision would show up as a `citation_accuracy` or `correctness` hit).

Interceptor wiring -- mandatory, not incidental
--------------------------------------------------
`score_answer` below calls the judge model exclusively through
`agents.llm.interceptor.LLMInterceptor.call()`, never `LLMClient.complete()` or
`complete_with_usage()` directly. This is a hard architectural rule for this module (per the
launching agent's binding instruction and `docs/LLD/eval.md`'s own "Interactions with other
modules" -- `agents/llm/` is named as "the shared final-answer LLM **and per-call cost/latency
interceptor** data source"): judge calls to a paid provider (OpenRouter/Gemini) are exactly the
kind of paid call the interceptor exists to track, and its output `StageRecord` is designed to
feed `agents/eval/cost_latency.py`'s `aggregate_by_stage`/`rollup_cost_per_1000_queries`
directly, with no adaptation layer -- so judge-call cost naturally rolls into the same
aggregation pipeline established by subtask 5.3.3, rather than becoming an invisible,
unaccounted-for cost source.

Single shared call path -- `pipeline.py`'s `generate_final_answer` precedent
--------------------------------------------------------------------------------
`agents/eval/pipeline.py` (5.2.4) established the "one shared function every caller funnels
through" pattern for final-answer generation, specifically to prevent per-caller prompt/config
divergence. `score_answer` below is this module's equivalent single call path: `score_arm_answers`
(and any future benchmark-run caller, e.g. `run_benchmark.py`/5.3.4) calls *this one function*
for every scored answer, never constructing its own judge prompt or its own interceptor call.

No live LLM calls in this module's own test suite -- disclosed constraint
------------------------------------------------------------------------------
Per this subtask's binding scoping constraint: `test_llm_judge.py` uses only a stub
`LLMClient` (mirroring `agents/eval/test_shared_final_llm.py`'s `_SpyLLMClient` convention) and
an `httpx.MockTransport`-backed `OllamaClient` (mirroring `agents/llm/test_interceptor.py`'s
convention) -- zero real network calls, zero `.env` reads, zero OpenRouter/Gemini API spend.
This module itself is provider-agnostic (it accepts any already-constructed `LLMClient`), so it
imposes no constraint of its own on which provider a real caller eventually uses.

No new dependency: only `dataclasses`/`json`/`collections.abc`/`typing` from the standard
library, matching `agents/eval/metrics.py` and `agents/eval/cost_latency.py`'s own "no new
dependency" convention -- `agents/pyproject.toml` is untouched.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from dataclasses import dataclass
from typing import TYPE_CHECKING

from eval.cost_latency import StageRecord

if TYPE_CHECKING:
    from llm.client import LLMClient
    from llm.interceptor import LLMInterceptor

#: The defined rubric this module scores every answer against -- see module docstring's
#: "Defined rubric" section. Each criterion is scored on a 1-5 integer scale by the judge model.
JUDGE_RUBRIC_CRITERIA: tuple[str, ...] = ("correctness", "completeness", "citation_accuracy")

#: Inclusive integer score range every rubric criterion must fall within.
_MIN_SCORE = 1
_MAX_SCORE = 5


class JudgeError(Exception):
    """Raised when a judge model's response cannot be parsed into a valid `JudgeScore`.

    Mirrors `agents/eval/cost_latency.py`'s `resolve_cost_usd` "refuse to invent data"
    convention: a missing rubric criterion, an out-of-range score, or unparseable JSON is
    treated as a hard failure, never silently coerced into some default/guessed score.
    """


def build_judge_prompt(query: str, answer: str, *, reference_context: str = "") -> str:
    """Render the rubric + query + answer (+ optional reference context) into a judge prompt.

    Args:
        query: The benchmark query the answer is responding to.
        answer: The candidate answer text to be judged (e.g. a
            `query.synthesizer.SynthesizerResult.answer`, per `pipeline.py`'s shared
            final-answer call path).
        reference_context: Optional supporting context (e.g. the `selected_markdown` the
            answer was generated from, per `pipeline.py`'s `_build_selected_markdown`) the
            judge may use to check `correctness`/`citation_accuracy`. Omitted from the prompt
            entirely when empty (the common case for a pure spot-check calibration sample that
            has no retrieval context attached).

    Returns:
        A prompt instructing the judge model to respond with strict JSON:
        `{"scores": {<criterion>: <1-5 int>, ...}, "rationale": "<string>"}`, covering exactly
        `JUDGE_RUBRIC_CRITERIA`.
    """
    criteria_lines = "\n".join(f"- {criterion} (1-5)" for criterion in JUDGE_RUBRIC_CRITERIA)
    context_section = f"\nReference context:\n{reference_context}\n" if reference_context else ""
    scores_shape = ", ".join(f'"{criterion}": <1-5 int>' for criterion in JUDGE_RUBRIC_CRITERIA)
    return (
        "You are an impartial judge scoring an answer's quality against the following rubric. "
        "Score each criterion on a 1-5 integer scale (1 = very poor, 5 = excellent).\n\n"
        f"Rubric criteria:\n{criteria_lines}\n\n"
        f"Query:\n{query}\n\n"
        f"Answer:\n{answer}\n"
        f"{context_section}\n"
        "Respond with strict JSON only, in exactly this shape:\n"
        f'{{"scores": {{{scores_shape}}}, "rationale": "<brief explanation>"}}'
    )


@dataclass(frozen=True)
class JudgeScore:
    """One judge model's rubric scoring of a single (query, answer) pair.

    Attributes:
        query: The query the scored answer was responding to.
        answer: The scored answer text.
        scores: Maps each `JUDGE_RUBRIC_CRITERIA` name to its 1-5 integer score.
        overall: Mean of `scores.values()` -- a single summary figure, kept alongside (not
            instead of) the per-criterion breakdown so callers needing one number (e.g.
            `calibrate_judge.py`'s delta-from-human-rating computation) don't have to
            re-derive it, matching `agents/eval/metrics.py::ArmScore`'s "convenience `@property`
            summary alongside per-item detail" pattern (implemented as a plain field here,
            since `overall` is computed once at parse time rather than lazily).
        rationale: The judge's free-text explanation for its scores.
    """

    query: str
    answer: str
    scores: Mapping[str, int]
    overall: float
    rationale: str

    def to_json(self) -> dict:
        return {
            "query": self.query,
            "answer": self.answer,
            "scores": dict(self.scores),
            "overall": self.overall,
            "rationale": self.rationale,
        }


def parse_judge_response(query: str, answer: str, raw_text: str) -> JudgeScore:
    """Parse a judge model's raw completion text into a `JudgeScore`.

    Args:
        query: The query the scored answer was responding to (carried into the result, not
            re-derived from `raw_text`).
        answer: The scored answer text (carried into the result, not re-derived from
            `raw_text`).
        raw_text: The judge model's raw completion text, expected to be the strict JSON shape
            `build_judge_prompt` requests: `{"scores": {<criterion>: <1-5 int>, ...},
            "rationale": "<string>"}`.

    Returns:
        A `JudgeScore` with `overall` computed as the mean of `scores.values()`.

    Raises:
        JudgeError: If `raw_text` is not valid JSON, is not a JSON object, is missing the
            `scores`/`rationale` keys, is missing any `JUDGE_RUBRIC_CRITERIA` entry in
            `scores`, or has any score outside `[1, 5]` (inclusive) or not an int. Never
            silently substitutes a default score for a missing/invalid one -- see module
            docstring's "refuse to invent data" convention.
    """
    try:
        parsed = json.loads(raw_text)
    except json.JSONDecodeError as exc:
        raise JudgeError(f"judge response is not valid JSON: {exc}\nraw response: {raw_text!r}") from exc

    if not isinstance(parsed, dict):
        raise JudgeError(f"judge response must be a JSON object, got {type(parsed).__name__}")

    if "scores" not in parsed or not isinstance(parsed["scores"], dict):
        raise JudgeError(f"judge response missing a 'scores' object: {raw_text!r}")
    raw_scores = parsed["scores"]

    rationale = parsed.get("rationale", "")
    if not isinstance(rationale, str):
        raise JudgeError(f"judge response 'rationale' must be a string, got {type(rationale).__name__}")

    scores: dict[str, int] = {}
    for criterion in JUDGE_RUBRIC_CRITERIA:
        if criterion not in raw_scores:
            raise JudgeError(
                f"judge response missing required rubric criterion {criterion!r}: {raw_text!r}"
            )
        value = raw_scores[criterion]
        if isinstance(value, bool) or not isinstance(value, int):
            raise JudgeError(
                f"judge response criterion {criterion!r} must be an int, got {value!r}"
            )
        if not (_MIN_SCORE <= value <= _MAX_SCORE):
            raise JudgeError(
                f"judge response criterion {criterion!r} score {value!r} out of range "
                f"[{_MIN_SCORE}, {_MAX_SCORE}]"
            )
        scores[criterion] = value

    overall = sum(scores.values()) / len(scores)
    return JudgeScore(query=query, answer=answer, scores=scores, overall=overall, rationale=rationale)


@dataclass(frozen=True)
class JudgeScoringResult:
    """Result of one `score_answer` call: the parsed score plus its cost/latency record.

    `record` is a `cost_latency.StageRecord` produced directly by `LLMInterceptor.call()` --
    ready to pass straight into `cost_latency.aggregate_by_stage`/
    `rollup_cost_per_1000_queries` alongside other calls' records, mirroring
    `agents.llm.interceptor.InterceptedCompletion`'s own "no adaptation layer needed" precedent.
    """

    score: JudgeScore
    record: StageRecord


def score_answer(
    query: str,
    answer: str,
    llm_client: "LLMClient",
    interceptor: "LLMInterceptor",
    *,
    arm: str,
    stage: str = "llm_judge",
    provider: str = "ollama",
    model: str | None = None,
    query_id: str | None = None,
    reference_context: str = "",
) -> JudgeScoringResult:
    """Score one (query, answer) pair with the judge model, tracked via `LLMInterceptor`.

    This is the single shared judge-scoring call path -- see module docstring's "Single shared
    call path" section. Every caller (`score_arm_answers` below, and any future
    `run_benchmark.py`/5.3.4 caller) must go through this function rather than building its own
    judge prompt or its own interceptor call, mirroring `agents/eval/pipeline.py`'s
    `generate_final_answer` precedent.

    Args:
        query: The benchmark query the answer is responding to.
        answer: The candidate answer text to be judged.
        llm_client: Any `LLMClient` implementation to call through (the judge model's client).
        interceptor: An `agents.llm.interceptor.LLMInterceptor` instance -- the judge call is
            made exclusively via `interceptor.call(...)`, never `llm_client.complete()` or
            `complete_with_usage()` directly (see module docstring's "Interceptor wiring"
            section).
        arm: Benchmark arm name for the resulting `StageRecord.arm` (e.g. `"hivemind"`,
            `"vector_rag"`), forwarded unchanged to `interceptor.call`.
        stage: Pipeline stage name for the resulting `StageRecord.stage`. Defaults to
            `"llm_judge"` so judge-call cost/latency is distinguishable from e.g. a
            `"final_answer"` stage record for the same arm.
        provider: Free-form provider label forwarded to `interceptor.call` (e.g. `"ollama"`,
            `"openrouter"`, `"gemini"`) -- determines whether the call is treated as free
            (`"ollama"`) or priced via the interceptor's rate table.
        model: Optional per-call model override, forwarded to `interceptor.call`.
        query_id: Optional query identifier, forwarded to `interceptor.call` (feeds
            `cost_latency.rollup_cost_per_1000_queries`'s per-query counting).
        reference_context: Optional supporting context forwarded to `build_judge_prompt`.

    Returns:
        A `JudgeScoringResult` with the parsed `JudgeScore` and the call's `StageRecord`.

    Raises:
        JudgeError: If the judge model's response cannot be parsed (see
            `parse_judge_response`).
        LLMError: Whatever `interceptor.call()` itself raises on a provider call failure.
        LLMInterceptorError: If `provider` is a non-free provider whose cost cannot be
            determined (see `agents.llm.interceptor.LLMInterceptorError`).
    """
    prompt = build_judge_prompt(query, answer, reference_context=reference_context)
    intercepted = interceptor.call(
        llm_client,
        provider=provider,
        arm=arm,
        stage=stage,
        prompt=prompt,
        model=model,
        query_id=query_id,
    )
    score = parse_judge_response(query, answer, intercepted.text)
    return JudgeScoringResult(score=score, record=intercepted.record)


def score_arm_answers(
    arm_name: str,
    answers: Mapping[str, str],
    llm_client: "LLMClient",
    interceptor: "LLMInterceptor",
    *,
    provider: str = "ollama",
    model: str | None = None,
    reference_context_by_query: Mapping[str, str] | None = None,
) -> list[JudgeScoringResult]:
    """Score every (query, answer) pair in `answers` for one benchmark arm.

    Mirrors `agents/eval/metrics.py::score_arm`'s "one call per query, same shape, same order"
    architecture (not a literal reuse of `ArmScore`, since judge scores are a structurally
    different result, but the same per-arm-loop-over-one-shared-per-item-function pattern),
    with every per-query call routed through the single `score_answer` path above.

    Args:
        arm_name: Benchmark arm name, forwarded to `score_answer`'s `arm` argument for every
            call (so every resulting `StageRecord.arm` in this batch is `arm_name`).
        answers: Maps each query string to that arm's answer text for it.
        llm_client: Forwarded to `score_answer` for every call.
        interceptor: Forwarded to `score_answer` for every call.
        provider: Forwarded to `score_answer` for every call.
        model: Forwarded to `score_answer` for every call.
        reference_context_by_query: Optional per-query reference context, looked up by query
            string; a query absent from this mapping (or `None` altogether) is scored with
            `reference_context=""`.

    Returns:
        One `JudgeScoringResult` per entry in `answers`, in `answers`' iteration order.
    """
    context_by_query = reference_context_by_query or {}
    return [
        score_answer(
            query,
            answer,
            llm_client,
            interceptor,
            arm=arm_name,
            provider=provider,
            model=model,
            query_id=query,
            reference_context=context_by_query.get(query, ""),
        )
        for query, answer in answers.items()
    ]
