"""Corpus-growth-checkpoint benchmark harness (issue #28, subtask 5.3.4).

Per issue #28's subtask 5.3.4 acceptance criteria: "Running all three arms at 20%, 50%, and
100% of the corpus ingested produces a degradation chart of recall/precision over corpus
growth, the key novelty result of the project." This module is the scripted benchmark run
(`run_benchmark.py --checkpoints 20,50,100`) that produces the data file `agents/eval/chart.py`
renders into that chart.

Binding scoping constraint for THIS implementation pass -- disclosed
---------------------------------------------------------------------
This module is built and fixture-tested against a tiny synthetic corpus + a stub/local-only
`LLMClient` (see `test_run_benchmark.py`). It has been run zero times against the real corpus
or any real paid API. The actual live/real-corpus execution of this harness is a deliberately
separate, later step (gated on a computed cost estimate), not part of this implementation pass.
See `run_benchmark`'s own docstring and this subtask's CDR handoff for exactly what a real run
would cost.

"All three arms" -- resolved, not guessed
--------------------------------------------
`agents/eval/pipeline.py`'s own module docstring enumerates its complete wrapper set as exactly
three: `run_hivemind_arm`, `run_vector_rag_arm`, `run_graphrag_lite_arm` (no
`run_vector_rag_rerank_arm` exists there). `docs/LLD/eval.md`'s "Retrieval arms" section also
names exactly three (HiveMind, classic vector RAG, simplified GraphRAG-style). This module
therefore benchmarks exactly `{hivemind, vector_rag, graphrag_lite}` -- `vector_rag_rerank.py`
(5.2.2) is a real, independently-tested baseline module but was never wired into `pipeline.py`'s
shared-final-answer arms, and wiring it in as a 4th arm here would mean inventing a new
`pipeline.py` wrapper, which is outside this subtask's impacted-module list
(`run_benchmark.py` + `chart.py` only).

Corpus-checkpoint-by-percentage -- new, minimal, disclosed
---------------------------------------------------------------
No existing utility in `agents/eval/` or `agents/ingestion/` slices a corpus by ingestion
percentage (`datasets.py` only supports a flat `limit=` truncation; `agents/ingestion/` has no
checkpoint concept at all). `eval.traversal_precision.CorpusGrowthCheckpoint` names the target
shape but explicitly defers "wiring real 20/50/100-ingested corpus snapshots into this shape" to
this exact subtask. `checkpoint_corpus`/`CorpusCheckpoint` below are the minimal, clearly-scoped
mechanism this module defines to satisfy that: a simple percentage-prefix slice of an ordered
`(doc_id, text)` list (deterministic, no reordering) -- not a claim that this is how a real
ingestion-order-based checkpoint should ultimately be derived from `agents/ingestion/`'s actual
dispatch/segment pipeline (a larger, separate concern left to a future subtask).

Reuse, not reimplementation
----------------------------
- Retrieval: `eval.baselines.vector_rag.retrieve_documents` / `VectorRagIndex.build` /
  `chunk_document`, and `eval.baselines.graphrag_lite.retrieve_documents` / `EntityGraph.build`
  are called directly, never reimplemented. The HiveMind arm's real retrieval
  (`query.pipeline.run_query_pipeline`, gRPC-backed) is out of `pipeline.py`'s own disclosed
  scope; this module mirrors that boundary by accepting an injected
  `hivemind_retriever` callable standing in for it (fixture tests supply a fixed-answer stub;
  a future real run wires the actual gRPC pipeline in without any change to this module's own
  orchestration logic).
- Scoring: `eval.metrics.score_arm` (5.3.1) -- every arm's `list[str]` ranked-doc-id output is
  scored identically, no arm-specific branch.
- Cost/latency: `eval.cost_latency.StageRecord` / `rollup_cost_per_1000_queries` (5.3.3).
- Judge scoring (optional, disabled by default): `eval.llm_judge.score_arm_answers`, which
  itself is the only call path in this whole module permitted to reach a real paid provider (it
  routes through `agents.llm.interceptor.LLMInterceptor.call()`, per issue #59). The
  final-answer text fed to the judge is produced via `eval.pipeline.generate_final_answer` --
  the exact shared final-answer function `pipeline.py`'s three arm wrappers themselves call, so
  this reuses that single call path rather than inventing a second one.
- Graph-expansion precision-hurts-or-not check (optional): `eval.traversal_precision.
  compare_precision_across_checkpoints` (5.4.1), fed `CorpusGrowthCheckpoint`s built directly
  from this module's own `CorpusCheckpoint.docs` -- no duplicate checkpoint shape.

Offline-only in this pass
-----------------------------
Final-answer generation in this harness's own tests always goes through a stub/local `LLMClient`
(never `OpenRouterClient`/`GeminiClient`), so every retrieval+final-answer `StageRecord` this
module builds directly uses `provider="ollama"` (resolved to `$0.0` by
`cost_latency.resolve_cost_usd`'s existing free-provider rule) rather than routing through
`LLMInterceptor` for that step -- `LLMInterceptor` is reserved exclusively for the optional judge
path above. No `.env` file is read anywhere in this module.
"""

from __future__ import annotations

import argparse
import json
import math
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from eval.baselines import graphrag_lite, vector_rag
from eval.baselines.graphrag_lite import DEFAULT_MAX_HOPS, EntityGraph
from eval.baselines.vector_rag import OllamaEmbeddingClient, VectorRagIndex, chunk_document
from eval.cost_latency import ArmCostSummary, StageRecord, rollup_cost_per_1000_queries
from eval.ground_truth import QueryLabel, build_ground_truth_dataset
from eval.metrics import ArmScore, score_arm
from eval.pipeline import generate_final_answer
from eval.traversal_precision import (
    CorpusGrowthCheckpoint,
    TraversalPrecisionComparison,
    compare_precision_across_checkpoints,
)

if TYPE_CHECKING:
    from eval.llm_judge import JudgeScoringResult
    from llm.client import LLMClient
    from llm.interceptor import LLMInterceptor

#: Canonical arm names this module benchmarks -- see module docstring's "'All three arms' --
#: resolved, not guessed" section.
HIVEMIND_ARM = "hivemind"
VECTOR_RAG_ARM = "vector_rag"
GRAPHRAG_LITE_ARM = "graphrag_lite"
ALL_ARMS = (HIVEMIND_ARM, VECTOR_RAG_ARM, GRAPHRAG_LITE_ARM)

#: Default corpus-growth checkpoints, per issue #28's acceptance criteria ("20%, 50%, and 100%
#: of the corpus ingested").
DEFAULT_CHECKPOINT_PERCENTAGES: tuple[int, ...] = (20, 50, 100)

#: Stage name used for every retrieval+final-answer `StageRecord` this module builds directly
#: (i.e. not via `LLMInterceptor`, see module docstring's "Offline-only in this pass" section).
RETRIEVAL_STAGE = "retrieval_and_final_answer"


class RunBenchmarkError(Exception):
    """Raised on malformed input to this module's own functions (e.g. an out-of-range
    checkpoint percentage)."""


@dataclass(frozen=True)
class CorpusCheckpoint:
    """One corpus-growth checkpoint: a label, its ingestion percentage, and the corpus snapshot
    at that percentage.

    `docs` matches `EntityGraph.build`'s / `chunk_document`'s own `(doc_id, text)` input shape
    (mirroring `ingestion.rawdoc.RawDocument.id`/`.text`, per those modules' own docstrings).
    """

    label: str
    pct: int
    docs: list[tuple[str, str]]

    def as_mapping(self) -> dict[str, str]:
        """Return this checkpoint's corpus as a plain `{doc_id: text}` mapping (the shape
        `eval.pipeline.generate_final_answer`/`_build_selected_markdown` expect)."""
        return dict(self.docs)

    def to_traversal_precision_checkpoint(self) -> CorpusGrowthCheckpoint:
        """Convert to `eval.traversal_precision.CorpusGrowthCheckpoint` -- same shape, reused
        directly (see module docstring's "Reuse, not reimplementation" section), not
        duplicated."""
        return CorpusGrowthCheckpoint(label=self.label, docs=list(self.docs))


def checkpoint_corpus(all_docs: Sequence[tuple[str, str]], pct: int) -> list[tuple[str, str]]:
    """Slice `all_docs` down to its first `pct`% (by document count), preserving input order.

    Minimal, clearly-scoped corpus-checkpoint mechanism -- see module docstring's
    "Corpus-checkpoint-by-percentage" section for why this (and not some more elaborate
    ingestion-order-derived scheme) is what this subtask defines.

    Args:
        all_docs: The full-corpus `(doc_id, text)` list, in some fixed, deterministic order
            (caller's responsibility -- this function does not reorder).
        pct: Ingestion percentage in `(0, 100]`.

    Returns:
        The first `ceil(len(all_docs) * pct / 100)` documents of `all_docs`, same order. Empty
        if `all_docs` is empty.

    Raises:
        RunBenchmarkError: If `pct` is not in `(0, 100]`.
    """
    if not (0 < pct <= 100):
        raise RunBenchmarkError(f"checkpoint_corpus: pct must be in (0, 100], got {pct!r}")
    if not all_docs:
        return []
    count = math.ceil(len(all_docs) * pct / 100)
    count = max(1, min(count, len(all_docs)))
    return list(all_docs[:count])


def build_checkpoints(
    all_docs: Sequence[tuple[str, str]],
    percentages: Sequence[int] = DEFAULT_CHECKPOINT_PERCENTAGES,
) -> list[CorpusCheckpoint]:
    """Build one `CorpusCheckpoint` per entry in `percentages` (default `(20, 50, 100)`),
    slicing `all_docs` via `checkpoint_corpus`.

    Returns:
        One `CorpusCheckpoint` per input percentage, in the same order, labeled `"{pct}pct"`.
    """
    return [
        CorpusCheckpoint(label=f"{pct}pct", pct=pct, docs=checkpoint_corpus(all_docs, pct))
        for pct in percentages
    ]


#: A per-checkpoint retriever: given a `QueryLabel`, return that arm's ranked, best-first
#: `list[str]` doc-id result for `query_label.query` (matching `retrieve_documents`'s shared
#: output shape across all baseline arms).
Retriever = Callable[[QueryLabel], list[str]]

#: Given a checkpoint's corpus mapping and the `CorpusCheckpoint` itself, build a `Retriever`
#: for one arm at that checkpoint (index/graph built once, reused across every query).
RetrieverFactory = Callable[[Mapping[str, str], CorpusCheckpoint], Retriever]

#: Given a `QueryLabel` and a checkpoint's corpus mapping, return the HiveMind arm's
#: already-retrieved ranked doc ids for that query -- standing in for the real gRPC-backed
#: `query.pipeline.run_query_pipeline()` (see module docstring's "Reuse, not reimplementation"
#: section, HiveMind bullet).
HivemindRetrieverFn = Callable[[QueryLabel, Mapping[str, str]], list[str]]


@dataclass(frozen=True)
class ArmSpec:
    """Bundles one arm's name with a `RetrieverFactory` that builds a per-checkpoint
    `Retriever` (so index/graph construction happens once per checkpoint, not once per
    query)."""

    name: str
    build_retriever: RetrieverFactory


def _build_vector_rag_retriever_factory(
    embed_client: OllamaEmbeddingClient, *, top_k: int
) -> RetrieverFactory:
    def build(corpus: Mapping[str, str], checkpoint: CorpusCheckpoint) -> Retriever:
        chunks = [
            chunk for doc_id, text in checkpoint.docs for chunk in chunk_document(doc_id, text)
        ]
        index = VectorRagIndex.build(chunks, embed_client)

        def retrieve(query_label: QueryLabel) -> list[str]:
            return vector_rag.retrieve_documents(
                query_label.query, index, embed_client, top_k=top_k
            )

        return retrieve

    return build


def _build_graphrag_lite_retriever_factory(
    llm_client: "LLMClient",
    *,
    top_k: int,
    max_hops: int = DEFAULT_MAX_HOPS,
    model: str | None = None,
) -> RetrieverFactory:
    def build(corpus: Mapping[str, str], checkpoint: CorpusCheckpoint) -> Retriever:
        graph = EntityGraph.build(checkpoint.docs, llm_client, model=model)

        def retrieve(query_label: QueryLabel) -> list[str]:
            return graphrag_lite.retrieve_documents(
                query_label.query, graph, llm_client, top_k=top_k, max_hops=max_hops, model=model
            )

        return retrieve

    return build


def _build_hivemind_retriever_factory(hivemind_retriever: HivemindRetrieverFn) -> RetrieverFactory:
    def build(corpus: Mapping[str, str], checkpoint: CorpusCheckpoint) -> Retriever:
        def retrieve(query_label: QueryLabel) -> list[str]:
            return hivemind_retriever(query_label, corpus)

        return retrieve

    return build


def default_arm_specs(
    *,
    hivemind_retriever: HivemindRetrieverFn,
    embed_client: OllamaEmbeddingClient,
    graphrag_llm_client: "LLMClient",
    top_k: int = 5,
    max_hops: int = DEFAULT_MAX_HOPS,
    model: str | None = None,
) -> list[ArmSpec]:
    """Build the canonical 3-arm `ArmSpec` list (see module docstring's "'All three arms'"
    section).

    Args:
        hivemind_retriever: Stand-in for the real HiveMind gRPC-backed retrieval pipeline (see
            `HivemindRetrieverFn`'s own docstring).
        embed_client: `OllamaEmbeddingClient` used to build/query the vector-RAG arm's index.
        graphrag_llm_client: `LLMClient` used for the GraphRAG-lite arm's entity extraction.
        top_k: Number of ranked document ids each arm retrieves per query.
        max_hops: `max_hops` for the GraphRAG-lite arm's traversal (defaults to
            `graphrag_lite.DEFAULT_MAX_HOPS`).
        model: Optional per-call LLM model override, forwarded to the GraphRAG-lite arm.

    Returns:
        `[hivemind, vector_rag, graphrag_lite]` `ArmSpec`s, in that order.
    """
    return [
        ArmSpec(HIVEMIND_ARM, _build_hivemind_retriever_factory(hivemind_retriever)),
        ArmSpec(VECTOR_RAG_ARM, _build_vector_rag_retriever_factory(embed_client, top_k=top_k)),
        ArmSpec(
            GRAPHRAG_LITE_ARM,
            _build_graphrag_lite_retriever_factory(
                graphrag_llm_client, top_k=top_k, max_hops=max_hops, model=model
            ),
        ),
    ]


@dataclass(frozen=True)
class JudgeConfig:
    """Optional judge-scoring wiring for `run_arm_at_checkpoint` (disabled unless explicitly
    passed). See module docstring's "Reuse, not reimplementation" -- judge bullet.

    Attributes:
        final_answer_llm_client: `LLMClient` used to actually generate the final-answer text fed
            to the judge, via `eval.pipeline.generate_final_answer` (the same shared function
            `pipeline.py`'s arm wrappers call).
        judge_llm_client: `LLMClient` the judge model itself runs on.
        interceptor: An `agents.llm.interceptor.LLMInterceptor` instance -- the judge call is
            made exclusively through this (via `eval.llm_judge.score_answer`/
            `score_arm_answers`), never a bare `llm_client.complete()` call. This is the only
            call path in this whole module permitted to reach a real paid provider.
        provider: Forwarded to `score_arm_answers`; defaults to `"ollama"` (offline/free) --
            override explicitly (and only in a real, cost-estimated run) to use a paid judge
            provider.
        model: Optional per-call model override, forwarded to both the final-answer call and
            the judge call.
    """

    final_answer_llm_client: "LLMClient"
    judge_llm_client: "LLMClient"
    interceptor: "LLMInterceptor"
    provider: str = "ollama"
    model: str | None = None


@dataclass(frozen=True)
class CheckpointArmResult:
    """One row of this module's output data file: one arm's scored result at one checkpoint."""

    checkpoint_label: str
    checkpoint_pct: int
    arm: str
    arm_score: ArmScore
    cost_summary: ArmCostSummary | None
    judge_results: "list[JudgeScoringResult] | None" = None

    @property
    def mean_judge_overall(self) -> float | None:
        """Mean `JudgeScore.overall` across `judge_results`, or `None` if judge scoring was not
        wired in for this row."""
        if not self.judge_results:
            return None
        return sum(r.score.overall for r in self.judge_results) / len(self.judge_results)

    def to_json(self) -> dict:
        row: dict = {
            "checkpoint_label": self.checkpoint_label,
            "checkpoint_pct": self.checkpoint_pct,
            "arm": self.arm,
            "k": self.arm_score.k,
            "num_queries": len(self.arm_score.per_query),
            "mean_recall": self.arm_score.mean_recall,
            "mean_precision": self.arm_score.mean_precision,
        }
        if self.cost_summary is not None:
            row["query_count"] = self.cost_summary.query_count
            row["total_cost_usd"] = self.cost_summary.total_cost_usd
            row["cost_per_1000_queries"] = self.cost_summary.cost_per_1000_queries
            row["stages"] = [
                {
                    "stage": stage.stage,
                    "call_count": stage.call_count,
                    "mean_duration_seconds": stage.mean_duration_seconds,
                }
                for stage in self.cost_summary.stages
            ]
        if self.judge_results is not None:
            row["mean_judge_overall"] = self.mean_judge_overall
        return row


def run_arm_at_checkpoint(
    arm_name: str,
    retriever: Retriever,
    queries: list[QueryLabel],
    corpus: Mapping[str, str],
    *,
    k: int,
    include_cross_reference: bool = True,
    checkpoint_label: str,
    checkpoint_pct: int,
    judge_config: JudgeConfig | None = None,
) -> tuple[CheckpointArmResult, list[StageRecord]]:
    """Run one arm's `retriever` over every entry in `queries` at one checkpoint, score the
    result, and optionally judge-score the arm's final answers.

    Args:
        arm_name: Benchmark arm name (e.g. `"hivemind"`, `"vector_rag"`, `"graphrag_lite"`).
        retriever: This arm's per-query retrieval callable at this checkpoint (from an
            `ArmSpec.build_retriever` call).
        queries: Ground-truth `QueryLabel`s to run and score against.
        corpus: This checkpoint's `{doc_id: text}` mapping.
        k: Recall/precision cutoff, forwarded to `eval.metrics.score_arm`.
        include_cross_reference: Forwarded to `eval.metrics.score_arm`.
        checkpoint_label: This checkpoint's free-form label (e.g. `"20pct"`).
        checkpoint_pct: This checkpoint's ingestion percentage.
        judge_config: Optional `JudgeConfig` -- if provided, also generates a final answer per
            query (via `eval.pipeline.generate_final_answer`) and judge-scores it (via
            `eval.llm_judge.score_arm_answers`), rolling the judge's `StageRecord`s into the
            returned stage-record list. `None` (default) skips judge scoring entirely.

    Returns:
        `(CheckpointArmResult, stage_records)` -- `stage_records` includes both this function's
        own directly-built retrieval+final-answer records and (if `judge_config` given) the
        judge's `LLMInterceptor`-produced records, ready to pass into
        `eval.cost_latency.rollup_cost_per_1000_queries` alongside every other arm/checkpoint's
        records for a combined rollup.
    """
    retrieved_by_query: dict[str, list[str]] = {}
    stage_records: list[StageRecord] = []

    for query_label in queries:
        start = time.perf_counter()
        retrieved_ids = retriever(query_label)
        duration = time.perf_counter() - start
        retrieved_by_query[query_label.query] = retrieved_ids
        stage_records.append(
            StageRecord(
                arm=arm_name,
                stage=RETRIEVAL_STAGE,
                duration_seconds=duration,
                provider="ollama",
                query_id=query_label.query,
            )
        )

    arm_score = score_arm(
        arm_name,
        retrieved_by_query,
        queries,
        k,
        include_cross_reference=include_cross_reference,
    )

    judge_results = None
    if judge_config is not None:
        from eval.llm_judge import score_arm_answers  # local import: optional path only

        answers = {
            query_label.query: generate_final_answer(
                query_label.query,
                retrieved_by_query.get(query_label.query, []),
                corpus,
                judge_config.final_answer_llm_client,
                model=judge_config.model,
            ).answer
            for query_label in queries
        }
        judge_results = score_arm_answers(
            arm_name,
            answers,
            judge_config.judge_llm_client,
            judge_config.interceptor,
            provider=judge_config.provider,
            model=judge_config.model,
        )
        stage_records.extend(r.record for r in judge_results)

    cost_summaries = rollup_cost_per_1000_queries(stage_records)
    cost_summary = cost_summaries[0] if cost_summaries else None

    result = CheckpointArmResult(
        checkpoint_label=checkpoint_label,
        checkpoint_pct=checkpoint_pct,
        arm=arm_name,
        arm_score=arm_score,
        cost_summary=cost_summary,
        judge_results=judge_results,
    )
    return result, stage_records


@dataclass(frozen=True)
class BenchmarkReport:
    """Full benchmark output: one `CheckpointArmResult` per (checkpoint, arm) pair, plus every
    underlying `StageRecord` (for a combined cross-arm/cross-checkpoint cost rollup if wanted),
    plus (if requested) the 5.4.1 traversal-precision comparison per checkpoint."""

    results: list[CheckpointArmResult]
    stage_records: list[StageRecord]
    traversal_precision_comparisons: list[TraversalPrecisionComparison] = field(
        default_factory=list
    )

    def to_json(self) -> dict:
        checkpoints_seen: dict[str, int] = {}
        for r in self.results:
            checkpoints_seen[r.checkpoint_label] = r.checkpoint_pct
        return {
            "checkpoints": [
                {"label": label, "pct": pct} for label, pct in checkpoints_seen.items()
            ],
            "rows": [r.to_json() for r in self.results],
            "traversal_precision": [
                {
                    "checkpoint_label": c.checkpoint_label,
                    "expansion_ever_hurt_precision": c.expansion_ever_hurt_precision,
                    "expansion_mean_precision": c.expansion_score.mean_precision,
                    "no_expansion_mean_precision": c.no_expansion_score.mean_precision,
                }
                for c in self.traversal_precision_comparisons
            ],
        }


def run_benchmark(
    checkpoints: list[CorpusCheckpoint],
    queries: list[QueryLabel],
    arm_specs: list[ArmSpec],
    *,
    k: int = 5,
    include_cross_reference: bool = True,
    judge_config: JudgeConfig | None = None,
) -> BenchmarkReport:
    """Run every arm in `arm_specs` at every checkpoint in `checkpoints`, scoring against
    `queries`.

    This is the core orchestration function the `--checkpoints` CLI (`main`, below) drives; it
    takes no CLI/filesystem dependency itself so it is directly unit-testable against a fixture
    corpus (see `test_run_benchmark.py`).

    Args:
        checkpoints: Corpus-growth checkpoints to run (e.g. from `build_checkpoints`).
        queries: Ground-truth `QueryLabel`s (e.g. `ground_truth.GroundTruthDataset.queries`),
            run identically at every checkpoint.
        arm_specs: Arms to run (e.g. from `default_arm_specs`).
        k: Recall/precision cutoff, forwarded to `eval.metrics.score_arm` for every arm.
        include_cross_reference: Forwarded to `eval.metrics.score_arm`.
        judge_config: Optional `JudgeConfig`, forwarded to every `run_arm_at_checkpoint` call.

    Returns:
        A `BenchmarkReport` with one `CheckpointArmResult` per (checkpoint, arm) pair, in
        `checkpoints` x `arm_specs` order.
    """
    results: list[CheckpointArmResult] = []
    all_stage_records: list[StageRecord] = []

    for checkpoint in checkpoints:
        corpus_map = checkpoint.as_mapping()
        for spec in arm_specs:
            retriever = spec.build_retriever(corpus_map, checkpoint)
            result, stage_records = run_arm_at_checkpoint(
                spec.name,
                retriever,
                queries,
                corpus_map,
                k=k,
                include_cross_reference=include_cross_reference,
                checkpoint_label=checkpoint.label,
                checkpoint_pct=checkpoint.pct,
                judge_config=judge_config,
            )
            results.append(result)
            all_stage_records.extend(stage_records)

    return BenchmarkReport(results=results, stage_records=all_stage_records)


def run_benchmark_with_traversal_precision(
    checkpoints: list[CorpusCheckpoint],
    queries: list[QueryLabel],
    arm_specs: list[ArmSpec],
    graphrag_llm_client: "LLMClient",
    *,
    k: int = 5,
    top_k: int = 5,
    include_cross_reference: bool = True,
    judge_config: JudgeConfig | None = None,
    expanded_max_hops: int = DEFAULT_MAX_HOPS,
    model: str | None = None,
) -> BenchmarkReport:
    """`run_benchmark`, plus the optional 5.4.1 graph-expansion-precision-hurts-or-not check
    (`eval.traversal_precision.compare_precision_across_checkpoints`) run across the same
    `checkpoints`.

    This is a thin wrapper, not a parallel benchmark path: it calls `run_benchmark` unchanged
    for the recall/precision/cost rows, then separately calls
    `compare_precision_across_checkpoints` (reused, not reimplemented) over
    `CorpusCheckpoint.to_traversal_precision_checkpoint()` conversions of the same checkpoints.
    """
    report = run_benchmark(
        checkpoints,
        queries,
        arm_specs,
        k=k,
        include_cross_reference=include_cross_reference,
        judge_config=judge_config,
    )
    traversal_checkpoints = [c.to_traversal_precision_checkpoint() for c in checkpoints]
    comparisons = compare_precision_across_checkpoints(
        traversal_checkpoints,
        queries,
        graphrag_llm_client,
        top_k=top_k,
        k=k,
        expanded_max_hops=expanded_max_hops,
        include_cross_reference=include_cross_reference,
        model=model,
    )
    return BenchmarkReport(
        results=report.results,
        stage_records=report.stage_records,
        traversal_precision_comparisons=comparisons,
    )


def write_benchmark_results(report: BenchmarkReport, path: str | Path) -> None:
    """Write `report` to `path` as JSON (creating parent directories as needed)."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report.to_json(), indent=2), encoding="utf-8")


def load_benchmark_results(path: str | Path) -> dict:
    """Load a benchmark data file written by `write_benchmark_results`, as a plain dict (the
    same shape `BenchmarkReport.to_json()` produces)."""
    path = Path(path)
    return json.loads(path.read_text(encoding="utf-8"))


def main(argv: list[str] | None = None) -> None:
    """CLI entry point: `run_benchmark.py --checkpoints 20,50,100`.

    NOT exercised end-to-end by this subtask's own test suite -- doing so would require a real
    corpus, a real local Ollama server, and (for HiveMind arm) real engine gRPC wiring, none of
    which this offline implementation pass is permitted to invoke live. This function is built
    and reviewable, wiring together `build_ground_truth_dataset`/`build_checkpoints`/
    `default_arm_specs`/`run_benchmark`/`write_benchmark_results`, but is left for a later,
    explicitly cost-estimated, real run.
    """
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--checkpoints",
        default=",".join(str(p) for p in DEFAULT_CHECKPOINT_PERCENTAGES),
        help="Comma-separated ingestion percentages, e.g. '20,50,100'.",
    )
    parser.add_argument("--manifest", default=None, help="Path to 5.1.2's manifest.json.")
    parser.add_argument("--out", default="agents/eval/benchmark_results.json")
    parser.add_argument("--k", type=int, default=5)
    parser.add_argument("--top-k", type=int, default=5)
    args = parser.parse_args(argv)

    percentages = [int(p.strip()) for p in args.checkpoints.split(",") if p.strip()]

    dataset = (
        build_ground_truth_dataset(manifest_path=args.manifest)
        if args.manifest
        else build_ground_truth_dataset()
    )
    # Real corpus text is not loaded here in this pass -- see module docstring. A real run would
    # additionally load the corpus documents (e.g. via `data/synthetic_corpus/generated/` +
    # `eval.datasets.load_dataset`) and pass their `(doc_id, text)` pairs to `build_checkpoints`.
    raise RunBenchmarkError(
        "main(): real-corpus wiring is deliberately not invoked in this implementation pass "
        "(see module docstring's binding scoping constraint); this CLI is built for a later, "
        "separately-authorized, cost-estimated real run."
    )


if __name__ == "__main__":
    main()
