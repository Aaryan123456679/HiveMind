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
