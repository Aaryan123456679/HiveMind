"""Tests for `agents/eval/cost_latency.py` (issue #28, subtask 5.3.3).

Per subtask 5.3.3's test spec: "feed fixture interceptor logs, assert correct per-stage latency
and $/1000-query rollups." Fixtures below are literal `StageRecord(...)` construction (matching
`test_metrics_recall_precision.py`'s precedent of hand-verified, literal Python fixtures rather
than parsing a serialized log format -- no such format is emitted anywhere in this repo yet; see
`cost_latency.py`'s own "disclosed scope finding" docstring section). Every expected value below
is computed by hand and asserted exactly (`==`), matching that same precedent.

Pure computation only -- no network, no LLM calls, no live interceptor wiring.
"""

from __future__ import annotations

import pytest

from eval.cost_latency import (
    ArmCostSummary,
    StageAggregate,
    StageRecord,
    aggregate_by_stage,
    resolve_cost_usd,
    rollup_cost_per_1000_queries,
)

# --- StageRecord validation ---


def test_stage_record_rejects_negative_duration():
    with pytest.raises(ValueError):
        StageRecord(arm="vector_rag", stage="embedding", duration_seconds=-0.1, provider="ollama")


def test_stage_record_rejects_negative_cost():
    with pytest.raises(ValueError):
        StageRecord(
            arm="vector_rag",
            stage="final_answer",
            duration_seconds=1.0,
            provider="openrouter",
            cost_usd=-0.01,
        )


def test_stage_record_accepts_zero_duration_and_cost():
    record = StageRecord(
        arm="hivemind", stage="final_answer", duration_seconds=0.0, provider="ollama", cost_usd=0.0
    )
    assert record.duration_seconds == 0.0
    assert record.cost_usd == 0.0


# --- resolve_cost_usd ---


def test_resolve_cost_usd_trusts_explicit_value():
    record = StageRecord(
        arm="vector_rag",
        stage="final_answer",
        duration_seconds=1.2,
        provider="openrouter",
        cost_usd=0.0042,
    )
    assert resolve_cost_usd(record) == 0.0042


def test_resolve_cost_usd_ollama_defaults_to_zero_when_unset():
    record = StageRecord(
        arm="hivemind", stage="final_answer", duration_seconds=0.8, provider="ollama"
    )
    assert resolve_cost_usd(record) == 0.0


def test_resolve_cost_usd_ollama_is_case_insensitive():
    record = StageRecord(
        arm="hivemind", stage="final_answer", duration_seconds=0.8, provider="Ollama"
    )
    assert resolve_cost_usd(record) == 0.0


def test_resolve_cost_usd_raises_for_missing_non_local_cost():
    record = StageRecord(
        arm="vector_rag", stage="final_answer", duration_seconds=1.0, provider="openrouter"
    )
    with pytest.raises(ValueError, match="refusing to invent"):
        resolve_cost_usd(record)


def test_resolve_cost_usd_raises_for_missing_gemini_cost():
    record = StageRecord(
        arm="graphrag_lite", stage="entity_extraction", duration_seconds=0.5, provider="gemini"
    )
    with pytest.raises(ValueError):
        resolve_cost_usd(record)


# --- aggregate_by_stage ---


def test_aggregate_by_stage_handles_different_stage_sets_per_arm():
    # vector-RAG: embedding + final_answer. vector-RAG+rerank: adds rerank.
    # GraphRAG-lite: entity_extraction + final_answer. HiveMind: final_answer only.
    records = [
        StageRecord("vector_rag", "embedding", 0.10, "ollama"),
        StageRecord("vector_rag", "final_answer", 1.00, "ollama"),
        StageRecord("vector_rag_rerank", "embedding", 0.10, "ollama"),
        StageRecord("vector_rag_rerank", "rerank", 0.30, "ollama"),
        StageRecord("vector_rag_rerank", "final_answer", 1.00, "ollama"),
        StageRecord("graphrag_lite", "entity_extraction", 0.50, "ollama"),
        StageRecord("graphrag_lite", "final_answer", 1.00, "ollama"),
        StageRecord("hivemind", "final_answer", 1.00, "ollama"),
    ]
    aggregates = aggregate_by_stage(records)
    stage_keys = [(a.arm, a.stage) for a in aggregates]
    assert stage_keys == [
        ("vector_rag", "embedding"),
        ("vector_rag", "final_answer"),
        ("vector_rag_rerank", "embedding"),
        ("vector_rag_rerank", "rerank"),
        ("vector_rag_rerank", "final_answer"),
        ("graphrag_lite", "entity_extraction"),
        ("graphrag_lite", "final_answer"),
        ("hivemind", "final_answer"),
    ]
    # vector-RAG+rerank has a rerank stage that no other arm has.
    rerank_agg = next(a for a in aggregates if a.stage == "rerank")
    assert rerank_agg.arm == "vector_rag_rerank"
    assert rerank_agg.call_count == 1
    # HiveMind arm has no embedding/rerank/entity_extraction stage at all.
    hivemind_stages = {a.stage for a in aggregates if a.arm == "hivemind"}
    assert hivemind_stages == {"final_answer"}


def test_aggregate_by_stage_computes_exact_totals_and_means():
    records = [
        StageRecord("vector_rag", "embedding", 0.10, "ollama"),
        StageRecord("vector_rag", "embedding", 0.20, "ollama"),
        StageRecord("vector_rag", "embedding", 0.30, "ollama"),
        StageRecord("vector_rag", "final_answer", 1.00, "openrouter", cost_usd=0.004),
        StageRecord("vector_rag", "final_answer", 2.00, "openrouter", cost_usd=0.006),
    ]
    aggregates = aggregate_by_stage(records)
    assert len(aggregates) == 2

    embedding_agg, final_answer_agg = aggregates
    assert embedding_agg.arm == "vector_rag"
    assert embedding_agg.stage == "embedding"
    assert embedding_agg.call_count == 3
    assert embedding_agg.total_duration_seconds == pytest.approx(0.6)
    assert embedding_agg.mean_duration_seconds == pytest.approx(0.2)
    assert embedding_agg.total_cost_usd == 0.0

    assert final_answer_agg.arm == "vector_rag"
    assert final_answer_agg.stage == "final_answer"
    assert final_answer_agg.call_count == 2
    assert final_answer_agg.total_duration_seconds == pytest.approx(3.0)
    assert final_answer_agg.mean_duration_seconds == pytest.approx(1.5)
    assert final_answer_agg.total_cost_usd == pytest.approx(0.01)


def test_aggregate_by_stage_empty_input_returns_empty_list():
    assert aggregate_by_stage([]) == []


def test_aggregate_by_stage_raises_for_unpriced_paid_provider_record():
    records = [
        StageRecord("vector_rag", "final_answer", 1.0, "openrouter"),
    ]
    with pytest.raises(ValueError):
        aggregate_by_stage(records)


# --- rollup_cost_per_1000_queries ---


def test_rollup_cost_per_1000_queries_mixed_providers():
    # vector_rag arm: 2 queries, each with a free ollama embedding stage and a paid
    # openrouter final_answer stage costing $0.004 and $0.006 respectively.
    # Total cost = $0.01 over 2 queries -> $5.00 per 1000 queries.
    records = [
        StageRecord("vector_rag", "embedding", 0.1, "ollama", query_id="q1"),
        StageRecord("vector_rag", "final_answer", 1.0, "openrouter", cost_usd=0.004, query_id="q1"),
        StageRecord("vector_rag", "embedding", 0.1, "ollama", query_id="q2"),
        StageRecord("vector_rag", "final_answer", 1.0, "openrouter", cost_usd=0.006, query_id="q2"),
        # hivemind arm: 1 query, fully ollama (free) -> $0 per 1000 queries.
        StageRecord("hivemind", "final_answer", 0.9, "ollama", query_id="q1"),
    ]
    summaries = rollup_cost_per_1000_queries(records)
    assert summaries == [
        ArmCostSummary(
            arm="vector_rag",
            query_count=2,
            total_cost_usd=0.01,
            cost_per_1000_queries=5.0,
            stages=(
                StageAggregate("vector_rag", "embedding", 2, 0.2, 0.1, 0.0),
                StageAggregate("vector_rag", "final_answer", 2, 2.0, 1.0, 0.01),
            ),
        ),
        ArmCostSummary(
            arm="hivemind",
            query_count=1,
            total_cost_usd=0.0,
            cost_per_1000_queries=0.0,
            stages=(StageAggregate("hivemind", "final_answer", 1, 0.9, 0.9, 0.0),),
        ),
    ]


def test_rollup_counts_distinct_query_ids():
    # Two stage records tagged with the *same* query_id belong to one query.
    records = [
        StageRecord("graphrag_lite", "entity_extraction", 0.5, "ollama", query_id="q1"),
        StageRecord("graphrag_lite", "final_answer", 1.0, "ollama", query_id="q1"),
        StageRecord("graphrag_lite", "entity_extraction", 0.5, "ollama", query_id="q2"),
        StageRecord("graphrag_lite", "final_answer", 1.0, "ollama", query_id="q2"),
    ]
    (summary,) = rollup_cost_per_1000_queries(records)
    assert summary.query_count == 2


def test_rollup_falls_back_to_per_record_counting_without_query_id():
    # No record carries a query_id -> each record counts as its own query occurrence.
    records = [
        StageRecord("vector_rag", "embedding", 0.1, "ollama"),
        StageRecord("vector_rag", "final_answer", 1.0, "ollama"),
    ]
    (summary,) = rollup_cost_per_1000_queries(records)
    assert summary.query_count == 2


def test_rollup_cost_per_1000_queries_empty_input_returns_empty_list():
    assert rollup_cost_per_1000_queries([]) == []


def test_rollup_cost_per_1000_queries_raises_for_unpriced_paid_provider_record():
    records = [
        StageRecord("graphrag_lite", "entity_extraction", 0.5, "gemini", query_id="q1"),
    ]
    with pytest.raises(ValueError):
        rollup_cost_per_1000_queries(records)
