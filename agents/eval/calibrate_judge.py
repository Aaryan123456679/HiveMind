"""Manual spot-check calibration harness for `llm_judge.py` (issue #28, subtask 5.3.2).

Per subtask 5.3.2's acceptance criteria: "a manual spot-check harness allowing a human to
compare judge scores against a sample of manually-rated answers for calibration." This module
is the "manual calibration script" half of the subtask (the LLM-judge scoring itself lives in
the sibling module `agents/eval/llm_judge.py`).

Shape -- disclosed design
---------------------------
A human reviewer manually rates a small sample of (query, answer) pairs on the same 1-5 scale
`llm_judge.JudgeScore.overall` uses (see `llm_judge.py`'s "Defined rubric" docstring section),
recording each as a `ManualRating`. `run_calibration` then runs `llm_judge.score_answer` (the
one shared judge-scoring call path -- see that module's own "Single shared call path" section)
over the same samples and reports, per sample, the delta between the judge's `overall` score and
the human's `human_score`, plus aggregate agreement statistics -- the "comparison report" the
test spec names.

No statistics dependency: `mean_absolute_delta`/`max_absolute_delta`/
`agreement_within_1_point` are computed directly with the standard library (no numpy/pandas),
matching `agents/eval/metrics.py` and `agents/eval/cost_latency.py`'s own "no new dependency"
convention -- `agents/pyproject.toml` is untouched. A full statistical-correlation figure (e.g.
Pearson's r) is not computed: with the small sample sizes ("5-10 topics", mirroring
`agents/eval/ground_truth.py`'s own manual-spot-check precedent) a correlation coefficient is
of limited statistical value and would need a dependency this module deliberately avoids; the
simpler delta-based agreement statistics are sufficient for a human reviewer to judge whether
the judge model is well-calibrated.

CLI vs. testable core -- disclosed design
--------------------------------------------
`run_calibration()` is a pure function taking an already-constructed `llm_client` and
`interceptor` -- this is what `test_llm_judge.py` exercises directly with a stub client, per
this subtask's binding "no live LLM calls in tests" scoping constraint. `main()` is the actual
runnable CLI (`python -m eval.calibrate_judge --ratings ... --out ...`) a human would invoke for
a real calibration pass: it constructs a real `LLMClient` via `llm.factory.create_llm_client`
and a real `LLMInterceptor`, then delegates to `run_calibration`. The test suite's CLI-smoke
test monkeypatches `create_llm_client` (not `run_calibration` itself) so `main()`'s own
argument-parsing/file-I/O/report-writing logic is exercised end-to-end while still making zero
real network calls -- this proves the script is genuinely "runnable" per the test spec without
spending any of this project's budget-capped OpenRouter/Gemini keys.

Deferred -- disclosed, not silently dropped
------------------------------------------------
A live calibration run against real judge-model (OpenRouter/Gemini) outputs and real
manually-rated answers is explicitly out of scope for this implementation pass (per the
launching agent's binding scoping constraint) -- deferred to a separate, deliberately-scoped
run with its own cost estimate. This module ships the harness structure, fully tested with
mocks; no real calibration data or real judge-model call is performed here.
"""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from eval.llm_judge import JudgeScore, score_answer

if TYPE_CHECKING:
    from llm.client import LLMClient
    from llm.interceptor import LLMInterceptor

#: Default location for a manual-ratings input file (human-authored; not generated here).
DEFAULT_RATINGS_PATH = Path(__file__).resolve().parent / "calibration_ratings.json"

#: Default output location for the comparison report this module produces.
DEFAULT_REPORT_PATH = Path(__file__).resolve().parent / "calibration_report.json"

#: Delta (in judge-score points) within which a judge score is considered "in agreement" with
#: the human rating, for the `agreement_within_1_point` summary statistic.
_AGREEMENT_THRESHOLD = 1.0


@dataclass(frozen=True)
class ManualRating:
    """One human reviewer's manual rating of a (query, answer) pair.

    `human_score` uses the same 1-5 scale as `llm_judge.JudgeScore.overall` (see that module's
    "Defined rubric" docstring section), so the two are directly comparable without any
    rescaling.
    """

    query: str
    answer: str
    human_score: float
    human_rationale: str = ""

    def to_json(self) -> dict:
        return {
            "query": self.query,
            "answer": self.answer,
            "human_score": self.human_score,
            "human_rationale": self.human_rationale,
        }


@dataclass(frozen=True)
class CalibrationSample:
    """One (manual rating, judge score) pair, plus the delta between them."""

    manual: ManualRating
    judge: JudgeScore
    delta: float

    def to_json(self) -> dict:
        return {
            "manual": self.manual.to_json(),
            "judge": self.judge.to_json(),
            "delta": self.delta,
        }


@dataclass(frozen=True)
class CalibrationReport:
    """Aggregate comparison report over a batch of `CalibrationSample`s.

    Attributes:
        samples: Per-sample judge-vs-human comparison, same order as the input ratings.
        n: Number of samples (`len(samples)`).
        mean_absolute_delta: Mean of `abs(sample.delta)` across `samples`. `0.0` if `samples`
            is empty.
        max_absolute_delta: Max of `abs(sample.delta)` across `samples`. `0.0` if `samples` is
            empty.
        agreement_within_1_point: Fraction of `samples` with `abs(sample.delta) <=
            _AGREEMENT_THRESHOLD` (1.0 point). `1.0` (vacuously) if `samples` is empty.
    """

    samples: list[CalibrationSample] = field(default_factory=list)
    n: int = 0
    mean_absolute_delta: float = 0.0
    max_absolute_delta: float = 0.0
    agreement_within_1_point: float = 1.0

    def to_json(self) -> dict:
        return {
            "samples": [s.to_json() for s in self.samples],
            "n": self.n,
            "mean_absolute_delta": self.mean_absolute_delta,
            "max_absolute_delta": self.max_absolute_delta,
            "agreement_within_1_point": self.agreement_within_1_point,
        }


def _build_report(samples: list[CalibrationSample]) -> CalibrationReport:
    if not samples:
        return CalibrationReport(samples=[], n=0)
    abs_deltas = [abs(s.delta) for s in samples]
    agreement = sum(1 for d in abs_deltas if d <= _AGREEMENT_THRESHOLD) / len(samples)
    return CalibrationReport(
        samples=samples,
        n=len(samples),
        mean_absolute_delta=sum(abs_deltas) / len(samples),
        max_absolute_delta=max(abs_deltas),
        agreement_within_1_point=agreement,
    )


def run_calibration(
    manual_ratings: list[ManualRating],
    llm_client: "LLMClient",
    interceptor: "LLMInterceptor",
    *,
    arm: str = "calibration",
    model: str | None = None,
    provider: str = "ollama",
) -> CalibrationReport:
    """Score each `ManualRating` with the judge model and report judge-vs-human agreement.

    Args:
        manual_ratings: Human-authored spot-check sample (e.g. loaded via
            `load_manual_ratings`).
        llm_client: Forwarded to `llm_judge.score_answer` for every sample.
        interceptor: Forwarded to `llm_judge.score_answer` for every sample -- judge calls are
            tracked via `LLMInterceptor`, never called directly (see `llm_judge.py`'s
            "Interceptor wiring" docstring section).
        arm: Benchmark arm label attached to every resulting `StageRecord` (defaults to
            `"calibration"`, distinguishing calibration-run cost/latency from a real benchmark
            arm's judge calls).
        model: Forwarded to `score_answer` for every sample.
        provider: Forwarded to `score_answer` for every sample.

    Returns:
        A `CalibrationReport` with one `CalibrationSample` per input rating (same order) plus
        aggregate agreement statistics.
    """
    samples = []
    for rating in manual_ratings:
        result = score_answer(
            rating.query,
            rating.answer,
            llm_client,
            interceptor,
            arm=arm,
            provider=provider,
            model=model,
        )
        delta = result.score.overall - rating.human_score
        samples.append(CalibrationSample(manual=rating, judge=result.score, delta=delta))
    return _build_report(samples)


def load_manual_ratings(path: str | Path) -> list[ManualRating]:
    """Load a human-authored manual-ratings JSON file (a list of `ManualRating.to_json()`-shaped
    objects).

    Raises:
        FileNotFoundError: If `path` does not exist.
        ValueError: If the JSON is malformed or missing required fields.
    """
    path = Path(path)
    text = path.read_text(encoding="utf-8")
    try:
        raw = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ValueError(f"{path}: invalid JSON: {exc}") from exc
    if not isinstance(raw, list):
        raise ValueError(f"{path}: expected a top-level JSON list of manual ratings")

    ratings = []
    for i, item in enumerate(raw):
        if not isinstance(item, dict):
            raise ValueError(f"{path}: ratings[{i}] is not a JSON object")
        for key in ("query", "answer", "human_score"):
            if key not in item:
                raise ValueError(f"{path}: ratings[{i}] missing required field {key!r}")
        ratings.append(
            ManualRating(
                query=item["query"],
                answer=item["answer"],
                human_score=float(item["human_score"]),
                human_rationale=item.get("human_rationale", ""),
            )
        )
    return ratings


def write_report(report: CalibrationReport, path: str | Path) -> None:
    """Write `report` to `path` as JSON (creating parent directories as needed)."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report.to_json(), indent=2), encoding="utf-8")


def main(argv: list[str] | None = None) -> None:
    """Runnable CLI: load manual ratings, run calibration against a real judge model, write a
    comparison report.

    See module docstring's "CLI vs. testable core" section -- this is the only function in this
    module that constructs a real `LLMClient`/`LLMInterceptor`; `run_calibration` itself takes
    both as arguments and performs no client construction of its own.
    """
    # Imported here (not at module top level) so this module's pure, test-exercised functions
    # (`run_calibration`, `load_manual_ratings`, `write_report`) impose no import-time
    # dependency on `llm.factory`/`llm.interceptor` beyond the `TYPE_CHECKING`-only references
    # already declared above; the test suite's CLI-smoke test monkeypatches this exact name
    # (`calibrate_judge.create_llm_client`) to avoid any real provider construction.
    from llm.factory import create_llm_client
    from llm.interceptor import LLMInterceptor

    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0] if __doc__ else "")
    parser.add_argument(
        "--ratings", default=str(DEFAULT_RATINGS_PATH), help="Path to a manual-ratings JSON file"
    )
    parser.add_argument(
        "--out", default=str(DEFAULT_REPORT_PATH), help="Output path for the comparison report"
    )
    parser.add_argument(
        "--provider", default="ollama", help="LLM provider to use as the judge model"
    )
    parser.add_argument("--model", default=None, help="Optional per-call model override")
    args = parser.parse_args(argv)

    manual_ratings = load_manual_ratings(args.ratings)
    llm_client = create_llm_client(args.provider)
    interceptor = LLMInterceptor()
    report = run_calibration(
        manual_ratings, llm_client, interceptor, model=args.model, provider=args.provider
    )
    write_report(report, args.out)
    print(
        f"Calibrated {report.n} manual rating(s) -> {args.out} "
        f"(mean_absolute_delta={report.mean_absolute_delta:.2f}, "
        f"agreement_within_1_point={report.agreement_within_1_point:.2%})"
    )


if __name__ == "__main__":
    main()
