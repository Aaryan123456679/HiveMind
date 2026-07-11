"""Per-stage latency + $/1000-query cost aggregation (issue #28, subtask 5.3.3).

Per subtask 5.3.3's acceptance criteria: "Latency and cost data from the agents/llm/ and
engine/rpc/ interceptors (task-3.2.4) is aggregated per pipeline stage and rolled up into a
$/1000-query figure per arm."

Disclosed scope finding -- what actually exists vs. what the issue text implies
--------------------------------------------------------------------------------
Task-3.2.4 (issue #16) shipped only the **Go-side** gRPC interceptor
(`engine/rpc/interceptor.go`'s `LatencyInterceptor`/`RPCMetric`/`Recorder`). That file's own
header comment explicitly records the Python-side counterpart as out of scope: "The
corresponding Python-side interceptor (agents/llm/interceptor.py or similar) is out of scope of
this dispatch." No later subtask has built it since (`agents/llm/` contains only the provider
clients -- `ollama_client.py`, `openrouter_client.py`, `gemini_client.py` -- and
`client.py`/`factory.py`; no interceptor, no token-usage capture, no cost recording anywhere in
that package). `docs/LLD/llm-provider.md` says as much itself: "No HTTP
logging/tracing/interceptor layer exists at this seam yet." There is also no pricing/rate-table
data anywhere in this repo for OpenRouter or Gemini token costs (`grep`-confirmed).

Given this module's impacted-modules list is `agents/eval/cost_latency.py` +
`agents/eval/test_cost_latency_aggregation.py` only (not `agents/llm/interceptor.py`, not a
cross-process log-shipping bridge, not a pricing table), and the test spec says "feed fixture
interceptor logs" (i.e. the test constructs records directly, no real interceptor wiring or log
parsing required), this module defines the **structured per-stage record shape** a real
interceptor -- Go `RPCMetric`-shaped, or a future `agents/llm/interceptor.py` -- would need to
emit for this aggregation to work, modeled directly on `RPCMetric`'s existing fields (a
call/stage name, a duration, an optional cost figure) plus what `docs/LLD/eval.md` already
names as the benchmark's two headline metrics (per-stage latency, $/1000-query cost). It does
not invent pricing data: a paid-provider ("openrouter"/"gemini") record with no recorded
``cost_usd`` is treated as missing data and rejected loudly (`ValueError`), never silently
priced via a made-up per-token rate. Only local ``"ollama"`` calls get an explicit, documented
$0 default, matching Ollama being a free local model per `docs/HLD.md`/`llm-provider.md`.

Arms have different stage sets -- disclosed design
----------------------------------------------------
Per `agents/eval/pipeline.py` (subtask 5.2.4) and the launching agent's own framing: vector-RAG
has `embedding` + `final_answer`; vector-RAG+rerank adds a `rerank` stage; GraphRAG-lite has
`entity_extraction` + `final_answer`; the HiveMind arm (pre-retrieved docs, per 5.2.4's own
disclosed scope boundary) has only `final_answer`. Nothing in this module hard-codes a fixed
stage list or a fixed arm list -- `arm` and `stage` are plain strings, and aggregation groups by
whatever `(arm, stage)` pairs actually appear in the given records.

No new dependency: only `dataclasses`/`collections.abc`/`typing` from the standard library --
`agents/pyproject.toml` is untouched, per this project's standing constraint (pure
aggregation/rollup logic over structured records, no pandas/numpy needed).
"""

from __future__ import annotations

from collections import OrderedDict
from collections.abc import Iterable
from dataclasses import dataclass

#: Providers treated as local/free by default when a record carries no explicit `cost_usd`.
#: Case-insensitive match against `StageRecord.provider`.
_FREE_LOCAL_PROVIDERS = frozenset({"ollama"})


@dataclass(frozen=True)
class StageRecord:
    """One per-stage-call interceptor record -- the "fixture interceptor log" entry unit.

    Mirrors `engine/rpc/interceptor.go`'s `RPCMetric` shape (a call/stage name, a duration, an
    optional cost figure) generalized to any pipeline stage (LLM call or RPC call) and any
    benchmark arm, since that is the only interceptor record shape that actually exists in this
    repo today (see module docstring's "disclosed scope finding").

    Attributes:
        arm: Benchmark arm name, e.g. "vector_rag", "vector_rag_rerank", "graphrag_lite",
            "hivemind". Free-form -- not restricted to a fixed enum, so future arms need no
            code change here.
        stage: Pipeline stage name, e.g. "embedding", "rerank", "entity_extraction",
            "final_answer", or an RPC method name like "rpc:GetFile". Free-form, since arms use
            different stage sets (see module docstring).
        duration_seconds: Wall-clock duration of this single call, in seconds. Must be `>= 0`.
        provider: Backend that served this call, e.g. "ollama", "openrouter", "gemini",
            "engine_rpc". Used only to decide the Ollama-is-free default in
            `resolve_cost_usd`; any string is accepted.
        cost_usd: Explicit per-call cost in USD, if the (real or fixture) interceptor already
            computed it -- e.g. from an OpenRouter/Gemini response's token-usage figures times
            that provider's own pricing, which only the provider client itself would know.
            `None` when the interceptor recorded no cost (the common case for local Ollama
            calls). Must be `>= 0` when given.
        query_id: Optional identifier for the benchmark query this call belongs to. Used only
            to count distinct queries for the $/1000-query rollup denominator. When omitted
            across all of an arm's records, `rollup_cost_per_1000_queries` falls back to
            counting one query per record (documented fallback -- see that function).
    """

    arm: str
    stage: str
    duration_seconds: float
    provider: str
    cost_usd: float | None = None
    query_id: str | None = None

    def __post_init__(self) -> None:
        if self.duration_seconds < 0:
            raise ValueError(
                f"StageRecord(arm={self.arm!r}, stage={self.stage!r}): "
                f"duration_seconds must be >= 0, got {self.duration_seconds!r}"
            )
        if self.cost_usd is not None and self.cost_usd < 0:
            raise ValueError(
                f"StageRecord(arm={self.arm!r}, stage={self.stage!r}): "
                f"cost_usd must be >= 0 when given, got {self.cost_usd!r}"
            )


def resolve_cost_usd(record: StageRecord) -> float:
    """Return the USD cost to attribute to a single `StageRecord`.

    Rules, in order (see module docstring's "disclosed scope finding" for why):
      1. If `record.cost_usd` is set, trust it as-is -- the interceptor (real or fixture)
         already computed it.
      2. Else, if `record.provider` is a known free/local provider (currently just "ollama",
         case-insensitively), return `0.0` explicitly -- local inference has no per-call
         monetary cost.
      3. Else, raise `ValueError`: a non-local-provider record with no recorded cost is missing
         data this module must not paper over by inventing a per-token pricing rate (no such
         rate table exists anywhere in this repo).
    """
    if record.cost_usd is not None:
        return record.cost_usd
    if record.provider.lower() in _FREE_LOCAL_PROVIDERS:
        return 0.0
    raise ValueError(
        f"StageRecord(arm={record.arm!r}, stage={record.stage!r}, "
        f"provider={record.provider!r}) has no cost_usd and is not a known free/local "
        f"provider ({sorted(_FREE_LOCAL_PROVIDERS)!r}); refusing to invent a per-token "
        f"pricing rate for it."
    )


@dataclass(frozen=True)
class StageAggregate:
    """Aggregated latency/cost figures for one `(arm, stage)` pair."""

    arm: str
    stage: str
    call_count: int
    total_duration_seconds: float
    mean_duration_seconds: float
    total_cost_usd: float


def aggregate_by_stage(records: Iterable[StageRecord]) -> list[StageAggregate]:
    """Group `records` by `(arm, stage)` and compute per-group latency/cost totals.

    Returns one `StageAggregate` per distinct `(arm, stage)` pair encountered, in first-seen
    order (deterministic -- no incidental dict/set-ordering surprises). Empty `records` yields
    an empty list.
    """
    groups: OrderedDict[tuple[str, str], _StageAccumulator] = OrderedDict()
    for record in records:
        key = (record.arm, record.stage)
        acc = groups.get(key)
        if acc is None:
            acc = _StageAccumulator()
            groups[key] = acc
        acc.call_count += 1
        acc.total_duration_seconds += record.duration_seconds
        acc.total_cost_usd += resolve_cost_usd(record)

    return [
        StageAggregate(
            arm=arm,
            stage=stage,
            call_count=acc.call_count,
            total_duration_seconds=acc.total_duration_seconds,
            mean_duration_seconds=acc.total_duration_seconds / acc.call_count,
            total_cost_usd=acc.total_cost_usd,
        )
        for (arm, stage), acc in groups.items()
    ]


@dataclass(frozen=True)
class ArmCostSummary:
    """Per-arm $/1000-query rollup, plus that arm's own per-stage breakdown."""

    arm: str
    query_count: int
    total_cost_usd: float
    cost_per_1000_queries: float
    stages: tuple[StageAggregate, ...]


def rollup_cost_per_1000_queries(records: Iterable[StageRecord]) -> list[ArmCostSummary]:
    """Group `records` by `arm` and compute a $/1000-query rollup for each.

    `query_count` for an arm is the number of distinct non-`None` `query_id` values seen among
    that arm's records. If *none* of an arm's records carry a `query_id` (all `None`), this
    falls back to counting one query per record -- a documented fallback for fixtures that
    don't bother tagging a query id, treating each stage-call record as its own query
    occurrence. Mixing tagged and untagged records within the same arm is not a supported input
    shape (distinct-`query_id` counting is used whenever at least one record is tagged).

    `total_cost_usd` sums `resolve_cost_usd` over the arm's records (so a paid-provider record
    with no cost recorded still raises `ValueError` here, same as in `aggregate_by_stage`).
    `cost_per_1000_queries = total_cost_usd / query_count * 1000`.

    Returns one `ArmCostSummary` per distinct `arm` encountered, in first-seen order. Empty
    `records` yields an empty list.
    """
    records = list(records)
    arm_order: list[str] = []
    per_arm_records: OrderedDict[str, list[StageRecord]] = OrderedDict()
    for record in records:
        if record.arm not in per_arm_records:
            arm_order.append(record.arm)
            per_arm_records[record.arm] = []
        per_arm_records[record.arm].append(record)

    summaries: list[ArmCostSummary] = []
    for arm in arm_order:
        arm_records = per_arm_records[arm]
        query_ids = {r.query_id for r in arm_records if r.query_id is not None}
        query_count = len(query_ids) if query_ids else len(arm_records)
        total_cost_usd = sum(resolve_cost_usd(r) for r in arm_records)
        cost_per_1000 = (total_cost_usd / query_count * 1000) if query_count else 0.0
        summaries.append(
            ArmCostSummary(
                arm=arm,
                query_count=query_count,
                total_cost_usd=total_cost_usd,
                cost_per_1000_queries=cost_per_1000,
                stages=tuple(aggregate_by_stage(arm_records)),
            )
        )
    return summaries


@dataclass
class _StageAccumulator:
    """Internal mutable accumulator used by `aggregate_by_stage`; not part of the public API."""

    call_count: int = 0
    total_duration_seconds: float = 0.0
    total_cost_usd: float = 0.0
