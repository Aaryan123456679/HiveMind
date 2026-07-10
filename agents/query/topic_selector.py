"""Topic selection: pick the top-`k` candidate topics by relevance.

Per issue #23 subtask 4.4.1 and `docs/LLD/query-agent.md`'s `topic_selector.py`
section ("Receives a candidate topic list from a non-LLM Go-side
`SearchCandidates` call ... Selects the top-`k` topics, where `k` is a
tunable hyperparameter (default 3)."), this module implements only the
top-`k` selection step. Two later subtasks on the same issue (4.4.2
graph-traversal expansion via `GraphNeighbors`, 4.4.3 hard-cap enforcement of
`k + 2k` total files) build on top of this module but are **not**
implemented here -- see "Extensibility, not over-engineering" below for how
this module's shape anticipates them without speculatively building their
behavior.

What does "a `SearchCandidates` result" look like here? -- disclosed choice
------------------------------------------------------------------------
`proto/hivemind.proto`'s real `SearchCandidatesResponse` is a
`repeated CandidateTopic candidates` message (`CandidateTopic{file_id,
path, score}`), and `agents/query/` has no gRPC client wiring yet (no
`wiring.py` analogue exists in this package the way `agents/ingestion/`
has one from task 3.4.4/3.4.2) -- building that channel/stub plumbing is out
of scope for 4.4.1's acceptance criteria and test spec, and is very likely a
later, dedicated subtask (e.g. issue #25) given the "impacted modules" list
for 4.4.1 names only `topic_selector.py`/`test_topic_selector.py`.

Following the closest precedent in this repo for the same
"already-fetched-candidates, now locally select from them" shape --
`agents/ingestion/shortlist.py` (task 3.4.2) -- this module defines its own
plain, frozen `TopicCandidate` dataclass decoupled from any gRPC-generated
type, so `select_top_k()` is unit-testable against a plain fixture list (per
4.4.1's own test spec: "against a fixture candidate list") with no need for
`grpc` to be installed or any mocked stub tree. Unlike `shortlist.py`,
4.4.1's test spec phrasing ("Given a SearchCandidates result, the selector
picks...") and acceptance criteria describe a function that receives an
*already-obtained* candidate list directly, not one that itself calls an
injected fetch function -- so `select_top_k()`'s signature takes
`Sequence[TopicCandidate]` directly rather than a `SearchCandidatesFn`
callable. The `SearchCandidatesFn` type alias is still declared below (never
called from this module) purely as a documented, reusable shape for whatever
later subtask does wire up the real RPC call and hands its decoded result to
`select_top_k()`.

Extensibility, not over-engineering -- disclosed choice
--------------------------------------------------------
The LLD's pipeline is "selector picks top-k, then may request 0-2 hop
expansion per selected topic judged insufficient alone, then the *combined*
result is hard-capped at k + 2k total files". `select_top_k()` is a plain
free function (not a class) because there is no state to carry across calls
yet -- 4.4.2 can compose on top of it by calling `select_top_k()` first and
then looping over its output to decide per-topic expansion; 4.4.3 can wrap
the combined (selected + expanded) sequence with its own cap function. No
shared-state object, config class, or unused expansion/cap parameters are
introduced now, since 4.4.1's acceptance criteria and test spec ask only for
top-k selection -- but the `DEFAULT_K` constant is named and exported (rather
than an inline literal) precisely so 4.4.2/4.4.3 can import and reuse it,
matching `ingestion.shortlist`'s `DEFAULT_TOP_K`/`DEFAULT_POOL_SIZE`
precedent of naming tunable bounds as module-level constants.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Sequence

#: Default number of topics `select_top_k` returns when the caller does not
#: override `k`. Per issue #23 subtask 4.4.1's acceptance criteria ("k
#: configurable parameter defaulting to 3").
DEFAULT_K = 3


@dataclass(frozen=True)
class TopicCandidate:
    """One candidate topic, decoupled from `proto/hivemind.proto`'s
    `CandidateTopic` message so callers/tests never need `grpc` or the
    generated stubs to construct or compare values.

    Field names/types mirror `CandidateTopic` (`file_id`, `path`, `score`)
    and `ingestion.shortlist.TopicCandidate`'s structurally-identical
    precedent, but this is an independent, module-local definition -- this
    package does not import from `agents/ingestion/`.
    """

    file_id: int
    path: str
    score: float


#: Injection-point shape for a future caller that fetches its own bounded
#: candidate pool via the engine's real `SearchCandidates` RPC: `(query,
#: max_results) -> candidates`. Mirrors `SearchCandidatesRequest{query,
#: max_results}` and `ingestion.shortlist.SearchCandidatesFn` exactly. Not
#: called anywhere in this module in this dispatch -- `select_top_k` takes
#: an already-obtained candidate sequence directly (see module docstring).
SearchCandidatesFn = Callable[[str, int], Sequence[TopicCandidate]]


def select_top_k(
    candidates: Sequence[TopicCandidate],
    *,
    k: int = DEFAULT_K,
) -> list[TopicCandidate]:
    """Return the top-`k` `candidates` by descending relevance `score`.

    Args:
        candidates: A `SearchCandidates` result already decoded into plain
            `TopicCandidate` values (e.g. a fixture list in tests, or a
            future caller's decoded `SearchCandidatesResponse.candidates`).
            Not mutated.
        k: Maximum number of topics to select. Must be >= 0. Defaults to
            `DEFAULT_K` (3), per issue #23 subtask 4.4.1's acceptance
            criteria.

    Returns:
        Up to `k` `TopicCandidate`s from `candidates`, sorted by descending
        `score` (ties broken by original input order, for determinism).
        Never larger than `min(k, len(candidates))`.

    Raises:
        ValueError: If `k` is negative.
    """
    if k < 0:
        raise ValueError(f"select_top_k: k must be >= 0, got {k}")

    if k == 0 or not candidates:
        return []

    ranked_indices = sorted(
        range(len(candidates)),
        key=lambda i: (-candidates[i].score, i),
    )
    top_indices = ranked_indices[:k]
    return [candidates[i] for i in top_indices]


# ---------------------------------------------------------------------------
# 4.4.2 -- graph-traversal expansion decision, delegating to GraphNeighbors
# ---------------------------------------------------------------------------
#
# Per issue #23 subtask 4.4.2 and `docs/LLD/query-agent.md`'s `topic_selector.py`
# section ("For any topic it judges insufficient alone, it may also request
# graph-traversal expansion (0-2 hops); the Go engine performs the expansion via
# `GraphNeighbors`."), this section adds the *decision* of which of `select_top_k`'s
# output topics warrant a `GraphNeighbors` expansion request, and delegates the actual
# expansion call to an injected function -- it does not implement graph traversal
# itself (that lives in `engine/graph`/`engine/rpc`), and it does not implement 4.4.3's
# `k + 2k` hard cap on the *combined* result (a later, separate subtask).
#
# "Judged insufficient alone" -- disclosed heuristic, since the LLD does not specify one
# ---------------------------------------------------------------------------------------
# Neither `docs/LLD/query-agent.md` nor the issue's acceptance criteria give a concrete
# rule for what makes a selected topic "insufficient alone". This module uses a simple,
# rules-based (not LLM) heuristic: a selected topic is flagged insufficient if its own
# `score` falls below `ratio * top_score`, where `top_score` is the highest score among
# the topics `select_top_k` actually returned for this query, and `ratio` is a tunable
# constant (`DEFAULT_INSUFFICIENCY_RATIO`, not a hardcoded literal in the comparison
# itself). This is deliberately *relative*, not an absolute cutoff: `score`'s scale is
# caller-defined (see `select_top_k`'s own docstring -- candidates come from an
# already-decoded `SearchCandidates` result whose relevance metric this module does not
# control), so a fixed absolute threshold would only be meaningful for one particular
# score distribution. A relative-to-top rule also guarantees the single best-selected
# topic is never flagged (its own score never falls below `ratio * itself` for any
# `ratio <= 1`), matching the intuitive reading that the top-ranked result is presumed
# the most self-sufficient of the selection.
#
# Hop depth -- disclosed choice: always request the max of the valid range
# ---------------------------------------------------------------------------
# `proto/hivemind.proto`'s `GraphNeighborsRequest.depth` and `engine/rpc/server.go`'s
# `Server.GraphNeighbors` handler both constrain `depth` to `[0, 2]` -- confirmed by
# reading the handler directly (`depth < 0 || depth > 2` -> `InvalidArgument`). Issue
# #23's "0-2 hop" phrasing is that same valid range, not a description of a per-call
# variable hop-selection rule -- neither the LLD nor the issue names any signal (topic
# type, score, edge density, etc.) for choosing 0 vs. 1 vs. 2 dynamically per topic, and
# inventing one would be unfounded speculation beyond this subtask's scope. This module
# therefore always requests `DEFAULT_EXPANSION_HOPS` (2, the max of the valid range) for
# every topic it flags as insufficient, on the reasoning that a topic already judged
# not self-sufficient should get the most graph context the engine allows. `hops`
# remains a caller-overridable, range-validated keyword parameter (not a literal
# baked into the call), so a future tunable config can change it without touching this
# module's signature -- mirroring `select_top_k`'s own `k` parameter precedent.
#
# DI shape -- mirrors `SearchCandidatesFn`'s precedent exactly
# ----------------------------------------------------------------
# `agents/query/` has no gRPC client wiring yet (no `wiring.py` analogue exists, same gap
# 4.4.1 disclosed for `SearchCandidatesFn`), and this subtask's test spec explicitly says
# "GraphNeighbors mocked" -- so, following `agents/ingestion/shortlist.py`'s
# `SearchCandidatesFn` pattern, `GraphNeighborsFn` is a plain `Callable` type alias
# (`(file_id, hops) -> Sequence[GraphNeighbor]`) that tests inject directly as a mock; no
# `Grpc*Client` wrapper is built in this dispatch, since it is not named in 4.4.2's
# impacted-modules list. `GraphNeighbor` is a frozen dataclass mirroring
# `proto/hivemind.proto`'s `Neighbor` message fields (`file_id`, `edge_type`, `weight`,
# `hop`), decoupled from the generated type so tests never need `grpc` installed --
# same rationale as `TopicCandidate`'s precedent in this same file.

#: Threshold ratio for `is_insufficient_alone`: a topic is flagged insufficient if its
#: own score is strictly less than `ratio * top_score` among the current selection. See
#: this section's module-level comment above for the full rationale.
DEFAULT_INSUFFICIENCY_RATIO = 0.5

#: Hop depth requested for every flagged expansion. `proto/hivemind.proto`'s
#: `GraphNeighborsRequest.depth` (enforced by `engine/rpc/server.go`'s `GraphNeighbors`
#: handler) is valid only in `[0, 2]`; this module always requests the max of that
#: range. See this section's module-level comment above for the full rationale.
DEFAULT_EXPANSION_HOPS = 2


@dataclass(frozen=True)
class GraphNeighbor:
    """One neighbor returned by a `GraphNeighbors` expansion, decoupled from
    `proto/hivemind.proto`'s `Neighbor` message so callers/tests never need `grpc` or
    the generated stubs.

    Field names/types mirror `Neighbor` (`target_file_id` -> `file_id`, `type` ->
    `edge_type`, `weight`, `hop`).
    """

    file_id: int
    edge_type: str
    weight: int
    hop: int


#: Injection point for a future caller's real `GraphNeighbors` RPC wrapper: given
#: `(file_id, hops)`, return the decoded neighbor list. Mirrors
#: `GraphNeighborsRequest{file_id, depth}` -> a sequence of `GraphNeighbor`. Tests
#: supply a plain mock callable here, per this subtask's test spec ("GraphNeighbors
#: mocked"); no real gRPC-backed implementation is built in this dispatch (see this
#: section's module-level comment above).
GraphNeighborsFn = Callable[[int, int], Sequence[GraphNeighbor]]


@dataclass(frozen=True)
class ExpansionResult:
    """One selected topic flagged insufficient alone, paired with the neighbors its
    `GraphNeighbors` expansion returned."""

    topic: TopicCandidate
    neighbors: list[GraphNeighbor]


def is_insufficient_alone(
    topic: TopicCandidate,
    top_score: float,
    *,
    ratio: float = DEFAULT_INSUFFICIENCY_RATIO,
) -> bool:
    """Return whether `topic` is judged "insufficient alone" relative to `top_score`.

    Args:
        topic: The selected topic to judge.
        top_score: The highest score among the current selection (typically
            `max(t.score for t in selected)`); `topic` is compared against this, not
            against a global/absolute constant. See this module's `DEFAULT_EXPANSION_HOPS`
            section-level comment for the full rationale.
        ratio: Threshold fraction. Defaults to `DEFAULT_INSUFFICIENCY_RATIO`.

    Returns:
        `True` if `topic.score < ratio * top_score`; `False` otherwise (strict `<`, so
        a topic exactly at the threshold -- including the top topic itself, whose score
        equals `top_score` -- is never flagged for any `ratio <= 1`).

    Raises:
        ValueError: If `ratio` is negative.
    """
    if ratio < 0:
        raise ValueError(f"is_insufficient_alone: ratio must be >= 0, got {ratio}")

    return topic.score < ratio * top_score


def expand_insufficient_topics(
    selected: Sequence[TopicCandidate],
    graph_neighbors: GraphNeighborsFn,
    *,
    hops: int = DEFAULT_EXPANSION_HOPS,
    ratio: float = DEFAULT_INSUFFICIENCY_RATIO,
) -> list[ExpansionResult]:
    """Decide which of `selected`'s topics are "insufficient alone" and request a
    `GraphNeighbors` expansion for exactly those, via `graph_neighbors`.

    Args:
        selected: Topics already chosen by `select_top_k` (or any equivalent sequence
            of `TopicCandidate`s). Not mutated. If empty, returns `[]` without calling
            `graph_neighbors` at all.
        graph_neighbors: Callable satisfying `GraphNeighborsFn` -- called as
            `graph_neighbors(topic.file_id, hops)` once per topic flagged insufficient,
            and *not at all* for topics judged sufficient (per this subtask's test
            spec: "expansion is requested only for topics flagged insufficient").
            Tests mock this directly; no real gRPC-backed implementation exists yet
            (see this module's section-level comment above).
        hops: Hop depth requested for every flagged expansion. Must be in `[0, 2]`,
            matching `engine/rpc/server.go`'s `GraphNeighbors` handler's own validated
            range. Defaults to `DEFAULT_EXPANSION_HOPS` (2).
        ratio: Forwarded to `is_insufficient_alone` as its `ratio` argument. Defaults to
            `DEFAULT_INSUFFICIENCY_RATIO`.

    Returns:
        One `ExpansionResult` per topic in `selected` (in original order) that was
        flagged insufficient, each carrying that call's decoded `graph_neighbors`
        result. Topics judged sufficient are simply absent from the result -- this
        function does not report "not expanded" entries.

    Raises:
        ValueError: If `hops` is outside `[0, 2]`, or if `ratio` is negative (raised by
            `is_insufficient_alone`).
    """
    if not 0 <= hops <= 2:
        raise ValueError(f"expand_insufficient_topics: hops must be in [0, 2], got {hops}")

    if not selected:
        return []

    top_score = max(topic.score for topic in selected)

    results: list[ExpansionResult] = []
    for topic in selected:
        if is_insufficient_alone(topic, top_score, ratio=ratio):
            neighbors = list(graph_neighbors(topic.file_id, hops))
            results.append(ExpansionResult(topic=topic, neighbors=neighbors))

    return results
