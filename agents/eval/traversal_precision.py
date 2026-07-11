"""Explicit graph-traversal-expansion-vs-precision check. Per issue #29 (milestone #7,
"Phase 5"), subtask 5.4.1. `docs/HLD.md` #7 ("System-wide known risks") names this directly:
"Graph traversal context blow-up -- bounded by a hard file-count cap of `k + 2k`; the benchmark
must measure whether traversal ever hurts precision, not just recall." `docs/LLD/eval.md`'s
"Known risks" section restates the same requirement: "the `agents/eval/` metrics must explicitly
check whether graph expansion hurts precision at any corpus-growth checkpoint, not just whether
it helps recall." This module is that check.

Reuse, not reimplementation
----------------------------
This module deliberately contains no new recall/precision math and no new graph-traversal logic:

- `agents/eval/metrics.py` (subtask 5.3.1) is the canonical home for `score_arm`/`ArmScore`/
  `QueryScore` (see that module's own docstring, "Canonical home" section) -- imported directly,
  not re-derived.
- `agents/eval/baselines/graphrag_lite.py` (subtask 5.2.3) already implements capped-hop,
  hop-decayed entity-graph traversal (`EntityGraph`, `retrieve_documents`, `DEFAULT_MAX_HOPS`).
  That module's own docstring states `max_hops=0` "disables hop expansion entirely (direct
  entity matches only)" -- this is exactly the "no-expansion" arm this subtask needs, so the
  "expansion" and "no-expansion" arms below are simply two calls to the same
  `retrieve_documents` with a different `max_hops`, never a second parallel retrieval
  implementation.

The "arm" being compared here is not a new fourth baseline arm alongside HiveMind/vector-RAG/
GraphRAG-lite (per `docs/LLD/eval.md`'s "Retrieval arms" section) -- it is an *ablation* of the
one existing GraphRAG-lite arm (with vs. without its own hop-expansion step), isolating exactly
the risk `docs/HLD.md` #7 calls out.

Corpus-growth-checkpoint scope boundary -- disclosed
--------------------------------------------------------
Issue #29's acceptance criteria ask for this comparison "at each corpus-growth checkpoint" (the
20%/50%/100%-ingested checkpoints `docs/LLD/eval.md`'s metrics section names as "the key novelty
result of the project"). Wiring those checkpoints against the real corpus
(`agents/eval/datasets.py`) is subtask 5.3.4's explicitly-gated "real benchmark execution" scope
(the only other subtask, alongside 5.3.2, authorized to make live paid-API calls) -- out of
scope here. This module instead models a "checkpoint" generically as an arbitrary label plus an
arbitrary `(doc_id, text)` corpus snapshot (`CorpusGrowthCheckpoint`), so
`compare_precision_across_checkpoints` can be pointed at real growth checkpoints later without
any change to this module's own comparison logic -- mirroring subtask 5.2.3's own disclosed
"fixture-only, corpus-wiring is future work" scope boundary. This subtask's own test spec asks
only for a fixture-scenario correctness check of the comparison logic itself, which is exactly
what this module's test file provides -- zero real-corpus wiring, zero live LLM calls (see
`test_traversal_precision_check.py`'s stub-client pattern, reused from
`test_graphrag_baseline.py`'s own established precedent).

Ollama-only / offline scope
------------------------------
This module makes no LLM calls of its own; it only forwards `llm_client`/`model` through to
`graphrag_lite.retrieve_documents` (which itself only ever calls `llm_client.complete`, a
provider-agnostic interface -- see that module's own "Ollama-only, direct-instantiation pattern"
docstring section). No network access, environment variable, or `.env` read happens in this
module, and its test file exercises it exclusively via a deterministic stub `LLMClient` (no
live Ollama, no OpenRouter/Gemini), per this implementation pass's standing offline-only
constraint.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from eval.baselines.graphrag_lite import DEFAULT_MAX_HOPS, EntityGraph, retrieve_documents
from eval.ground_truth import QueryLabel
from eval.metrics import ArmScore, score_arm
from llm.client import LLMClient

#: Forcing `graphrag_lite.retrieve_documents`'s `max_hops` to this value yields the
#: "no-expansion" arm: direct entity matches only, per that module's own docstring.
NO_EXPANSION_MAX_HOPS = 0


def retrieve_for_expansion_arms(
    query: str,
    graph: EntityGraph,
    llm_client: LLMClient,
    *,
    top_k: int,
    expanded_max_hops: int = DEFAULT_MAX_HOPS,
    model: str | None = None,
) -> tuple[list[str], list[str]]:
    """Retrieve `query`'s ranked doc ids under both the expansion arm and the no-expansion arm.

    Both calls go through the same `graphrag_lite.retrieve_documents` (see module docstring's
    "Reuse, not reimplementation" section) -- the only difference is `max_hops`.

    Returns:
        `(expansion_doc_ids, no_expansion_doc_ids)`, each a ranked, best-first `list[str]`
        truncated to `top_k`, matching `retrieve_documents`'s own output shape.
    """
    expansion_doc_ids = retrieve_documents(
        query, graph, llm_client, top_k=top_k, max_hops=expanded_max_hops, model=model
    )
    no_expansion_doc_ids = retrieve_documents(
        query, graph, llm_client, top_k=top_k, max_hops=NO_EXPANSION_MAX_HOPS, model=model
    )
    return expansion_doc_ids, no_expansion_doc_ids


@dataclass(frozen=True)
class QueryPrecisionDelta:
    """Precision@k for one query, with vs. without graph-traversal expansion."""

    query: str
    topic_id: str
    expansion_precision: float
    no_expansion_precision: float

    @property
    def delta(self) -> float:
        """`expansion_precision - no_expansion_precision`. Negative means expansion hurt
        precision for this query relative to not expanding at all."""
        return self.expansion_precision - self.no_expansion_precision

    @property
    def expansion_decreased_precision(self) -> bool:
        """`True` iff the expansion arm's precision@k is strictly lower than the no-expansion
        arm's for this query -- the exact condition `docs/HLD.md` #7 asks the benchmark to
        catch."""
        return self.expansion_precision < self.no_expansion_precision


@dataclass(frozen=True)
class TraversalPrecisionComparison:
    """Full expansion-vs-no-expansion precision comparison for one checkpoint (an arbitrary
    corpus snapshot / label -- see module docstring's "corpus-growth-checkpoint scope boundary"
    section)."""

    checkpoint_label: str
    expansion_score: ArmScore
    no_expansion_score: ArmScore
    per_query_deltas: list[QueryPrecisionDelta] = field(default_factory=list)

    @property
    def decreased_queries(self) -> list[QueryPrecisionDelta]:
        """The subset of `per_query_deltas` where expansion decreased precision."""
        return [d for d in self.per_query_deltas if d.expansion_decreased_precision]

    @property
    def expansion_ever_hurt_precision(self) -> bool:
        """`True` iff expansion decreased precision for at least one query at this checkpoint --
        the per-checkpoint flag `docs/LLD/eval.md`'s known-risks section asks for."""
        return len(self.decreased_queries) > 0


def compare_traversal_precision(
    graph: EntityGraph,
    queries: list[QueryLabel],
    llm_client: LLMClient,
    *,
    top_k: int,
    k: int,
    expanded_max_hops: int = DEFAULT_MAX_HOPS,
    include_cross_reference: bool = True,
    checkpoint_label: str = "default",
    model: str | None = None,
) -> TraversalPrecisionComparison:
    """Compare precision@k with vs. without graph-traversal expansion, across all `queries`,
    for one checkpoint (`graph`).

    Args:
        graph: An `EntityGraph` built (via `EntityGraph.build`) over the checkpoint's corpus
            snapshot.
        queries: Ground-truth `QueryLabel`s to score both arms against (e.g.
            `ground_truth.GroundTruthDataset.queries`).
        llm_client: `LLMClient` used for query-entity extraction (forwarded to
            `retrieve_documents`; see module docstring's "Ollama-only / offline scope" section).
        top_k: Number of ranked document ids each arm retrieves per query.
        k: Recall/precision cutoff (forwarded to `eval.metrics.score_arm`).
        expanded_max_hops: `max_hops` for the expansion arm (defaults to `graphrag_lite.
            DEFAULT_MAX_HOPS`). The no-expansion arm always uses `NO_EXPANSION_MAX_HOPS` (`0`).
        include_cross_reference: Forwarded to `eval.metrics.score_arm`.
        checkpoint_label: Free-form label identifying this checkpoint (e.g. `"20pct"`,
            `"50pct"`, `"100pct"`, or a fixture-test label) -- purely descriptive, not
            interpreted by this function.
        model: Optional per-call LLM model override, forwarded to `retrieve_documents`.

    Returns:
        A `TraversalPrecisionComparison` with both arms' `ArmScore`s and a per-query precision
        delta.
    """
    expansion_retrieved: dict[str, list[str]] = {}
    no_expansion_retrieved: dict[str, list[str]] = {}
    for query_label in queries:
        expansion_ids, no_expansion_ids = retrieve_for_expansion_arms(
            query_label.query,
            graph,
            llm_client,
            top_k=top_k,
            expanded_max_hops=expanded_max_hops,
            model=model,
        )
        expansion_retrieved[query_label.query] = expansion_ids
        no_expansion_retrieved[query_label.query] = no_expansion_ids

    expansion_score = score_arm(
        "graphrag_lite_expansion",
        expansion_retrieved,
        queries,
        k,
        include_cross_reference=include_cross_reference,
    )
    no_expansion_score = score_arm(
        "graphrag_lite_no_expansion",
        no_expansion_retrieved,
        queries,
        k,
        include_cross_reference=include_cross_reference,
    )

    per_query_deltas = [
        QueryPrecisionDelta(
            query=expansion_query.query,
            topic_id=expansion_query.topic_id,
            expansion_precision=expansion_query.precision,
            no_expansion_precision=no_expansion_query.precision,
        )
        for expansion_query, no_expansion_query in zip(
            expansion_score.per_query, no_expansion_score.per_query
        )
    ]

    return TraversalPrecisionComparison(
        checkpoint_label=checkpoint_label,
        expansion_score=expansion_score,
        no_expansion_score=no_expansion_score,
        per_query_deltas=per_query_deltas,
    )


@dataclass(frozen=True)
class CorpusGrowthCheckpoint:
    """One corpus-growth checkpoint: a label plus a corpus snapshot.

    `docs` matches `EntityGraph.build`'s own input shape (`(doc_id, text)` pairs, mirroring
    `ingestion.rawdoc.RawDocument.id`/`.text` -- see `graphrag_lite.py`'s own docstring). Wiring
    real 20%/50%/100%-ingested corpus snapshots into this shape is deferred (see module
    docstring's "corpus-growth-checkpoint scope boundary" section); fixture/test callers may
    construct this directly with a small inline corpus.
    """

    label: str
    docs: list[tuple[str, str]]


def compare_precision_across_checkpoints(
    checkpoints: list[CorpusGrowthCheckpoint],
    queries: list[QueryLabel],
    llm_client: LLMClient,
    *,
    top_k: int,
    k: int,
    expanded_max_hops: int = DEFAULT_MAX_HOPS,
    include_cross_reference: bool = True,
    model: str | None = None,
) -> list[TraversalPrecisionComparison]:
    """Run `compare_traversal_precision` independently at each of `checkpoints`.

    Builds a fresh `EntityGraph` per checkpoint (via `EntityGraph.build`) so each checkpoint's
    comparison reflects only that checkpoint's own corpus snapshot.

    Returns:
        One `TraversalPrecisionComparison` per input checkpoint, same order.
    """
    results = []
    for checkpoint in checkpoints:
        graph = EntityGraph.build(checkpoint.docs, llm_client, model=model)
        results.append(
            compare_traversal_precision(
                graph,
                queries,
                llm_client,
                top_k=top_k,
                k=k,
                expanded_max_hops=expanded_max_hops,
                include_cross_reference=include_cross_reference,
                checkpoint_label=checkpoint.label,
                model=model,
            )
        )
    return results


def checkpoints_with_precision_decrease(
    comparisons: list[TraversalPrecisionComparison],
) -> list[TraversalPrecisionComparison]:
    """Filter `comparisons` down to those where expansion decreased precision for at least one
    query -- the "reports any checkpoint where expansion decreases precision" acceptance
    criterion (issue #29, subtask 5.4.1)."""
    return [comparison for comparison in comparisons if comparison.expansion_ever_hurt_precision]
