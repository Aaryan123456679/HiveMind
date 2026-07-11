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
        equals `top_score` -- is never flagged for any `ratio <= 1`, regardless of
        `top_score`'s sign). When `top_score <= 0` this is enforced by always returning
        `False` (see the guard below), since "a fraction of `top_score`" is not a
        well-defined sufficiency floor once `top_score` is non-positive.

    Raises:
        ValueError: If `ratio` is negative.
    """
    if ratio < 0:
        raise ValueError(f"is_insufficient_alone: ratio must be >= 0, got {ratio}")

    if top_score <= 0:
        # `ratio * top_score` only shrinks the threshold toward a stricter
        # (smaller-magnitude) floor when `top_score` is positive: for `ratio` in
        # `[0, 1]`, `ratio * top_score <= top_score` iff `top_score >= 0`. When
        # `top_score` is negative, multiplying by a fraction in `[0, 1]` moves the
        # threshold *up* (less negative), so `ratio * top_score > top_score` -- which
        # would flag the top topic itself (`topic.score == top_score`) as
        # insufficient, violating this function's core invariant (see `Returns`
        # above). `top_score == 0` is equally degenerate (the threshold is always
        # exactly `0` no matter the ratio). There is no well-defined positive score to
        # take a fraction of in either case, so conservatively treat every topic as
        # sufficient (never flagged) whenever `top_score <= 0`.
        return False

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


# ---------------------------------------------------------------------------
# 4.4.3 -- hard-cap enforcement on the combined (selected + expanded) result
# ---------------------------------------------------------------------------
#
# Per issue #23 subtask 4.4.3 and `docs/LLD/query-agent.md`'s `topic_selector.py` section
# ("The combined result is hard-capped at `k + 2k` total files to prevent context blow-up
# -- this is a system-wide invariant, not just an implementation detail"; also restated in
# `docs/HLD.md`'s "System-wide known risks" section), this section combines `select_top_k`'s
# output with `expand_insufficient_topics`'s output into one final file list, and enforces
# that hard cap. Neither the LLD nor the issue names a concrete function -- disclosed choice
# below.
#
# Return shape -- disclosed choice: `list[int]` file_ids, not `list[TopicCandidate]`
# ---------------------------------------------------------------------------
# `selected` yields `TopicCandidate` (has `path`/`score`); `ExpansionResult.neighbors` yields
# `GraphNeighbor` (has `edge_type`/`weight`/`hop`, no `path`/`score`). The only field shared by
# both source types is `file_id`, so there is no common richly-typed shape to return without
# fabricating fields for one side or the other. The issue's acceptance criteria and test spec
# are phrased purely in terms of a *file-count* invariant ("final result length"), not in terms
# of preserved per-item metadata, so this module returns the final deduplicated, capped list of
# `file_id`s the query pipeline will fetch as context. Mapping `file_id`s back to file content
# for the synthesizer prompt is left to a later subtask (out of scope here).
#
# Dedup -- disclosed choice: a file is counted once even if reachable both ways
# ---------------------------------------------------------------------------
# Issue wording: "the final *selected-file set* never exceeds k+2k total files"; LLD wording:
# "The *combined result* is hard-capped at k + 2k total files". Both describe a set of distinct
# files, not a multiset of (topic, neighbor) pairs. A file that is one of the top-k selections
# AND is also returned as a `GraphNeighbor` of a *different* insufficient topic's expansion is
# still physically one file -- counting it twice would inflate the reported context size without
# adding new content, undermining the cap's own stated purpose ("prevent context blow-up").
# Dedup by `file_id` is therefore applied before truncation, not after.
#
# Priority -- disclosed choice: selected topics are collected before expansion neighbors
# ---------------------------------------------------------------------------
# Top-k selected topics are the primary, directly-relevant results; expansion neighbors are
# secondary, exploratory graph context requested only because a topic was judged insufficient
# alone. If the combined, deduplicated pool exceeds `k + 2k`, truncation must never silently
# drop a directly-selected topic's own file in favor of someone else's expansion neighbor. This
# function therefore walks `selected` first (in the order given, i.e. `select_top_k`'s own
# descending-score order), then `expansions` in the order given (per-topic order from
# `expand_insufficient_topics`, and within each topic's neighbors, the engine's own
# `GraphNeighborsFn` ordering, left untouched). First-seen `file_id` wins; later duplicates
# (whether against an earlier selected id or an earlier neighbor id) are skipped without
# recounting. The deduplicated, order-preserved list is then truncated to `k + 2k` entries.
#
# Cap formula -- disclosed choice: `k + 2k`, using this function's own `k` parameter
# ---------------------------------------------------------------------------
# The multiplier (`+2k`) is fixed by the LLD/issue wording, not tunable, so no new
# `DEFAULT_CAP_MULTIPLIER`-style constant is introduced -- inventing a tunable multiplier would
# be unfounded speculation beyond this subtask's scope, mirroring 4.4.1/4.4.2's own "disclosed
# choice, not over-engineering" precedent. `k` defaults to `DEFAULT_K` (imported, not
# redefined), matching `select_top_k`'s own `k` parameter, so a caller can pass the identical
# `k` used for `select_top_k` to get a consistent cap.


def combine_and_cap(
    selected: Sequence[TopicCandidate],
    expansions: Sequence[ExpansionResult],
    *,
    k: int = DEFAULT_K,
) -> list[int]:
    """Combine `selected` topics and `expansions`' neighbors into one final, deduplicated,
    hard-capped list of `file_id`s.

    Args:
        selected: Topics chosen by `select_top_k` (or an equivalent sequence of
            `TopicCandidate`s). Not mutated. Walked first, in the order given, so its
            `file_id`s always take priority over expansion neighbors when truncating.
        expansions: Result of `expand_insufficient_topics` (or an equivalent sequence of
            `ExpansionResult`s). Not mutated. Walked second, in the order given (each
            `ExpansionResult`'s `.neighbors` walked in the order given).
        k: Same tunable hyperparameter used for `select_top_k`. Must be >= 0. The hard cap is
            `k + 2 * k` total files. Defaults to `DEFAULT_K` (3), giving a default cap of 9.

    Returns:
        A list of `file_id`s (`int`), deduplicated (each distinct file counted once even if it
        appears in both `selected` and one or more `expansions`, or in more than one
        `ExpansionResult`), order-preserved (`selected`'s file_ids first in `selected`'s own
        order, then newly-seen expansion neighbor file_ids in `expansions`'s own order), and
        truncated to at most `k + 2 * k` entries. Never longer than
        `min(k + 2 * k, number of distinct file_ids across selected and expansions)`.

    Raises:
        ValueError: If `k` is negative.
    """
    if k < 0:
        raise ValueError(f"combine_and_cap: k must be >= 0, got {k}")

    cap = k + 2 * k

    combined: list[int] = []
    seen: set[int] = set()

    for topic in selected:
        if topic.file_id not in seen:
            seen.add(topic.file_id)
            combined.append(topic.file_id)

    for expansion in expansions:
        for neighbor in expansion.neighbors:
            if neighbor.file_id not in seen:
                seen.add(neighbor.file_id)
                combined.append(neighbor.file_id)

    return combined[:cap]
