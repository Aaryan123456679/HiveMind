"""Tests for `query.topic_selector.combine_and_cap`.

Per issue #23 subtask 4.4.3's test spec: "pytest agents/query/test_topic_selector_cap.py: feed
an oversized candidate+expansion set, assert final result length == min(available, k+2k)."
Also covers the dedup and selected-priority requirements disclosed in
`.cdr/runs/2026-07-11/020-implementation/architecture-discovery.md` and `plan.md` (a file
reachable both as a direct top-k selection and as an expansion neighbor of a different topic
must count once toward the cap), and the boundary/validation behavior needed to pin down
`combine_and_cap`'s contract.
"""

from __future__ import annotations

import pytest

from query.topic_selector import (
    DEFAULT_K,
    ExpansionResult,
    GraphNeighbor,
    TopicCandidate,
    combine_and_cap,
)


def _topic(file_id: int, score: float = 1.0) -> TopicCandidate:
    return TopicCandidate(file_id=file_id, path=f"p/{file_id}", score=score)


def _neighbor(file_id: int, hop: int = 1) -> GraphNeighbor:
    return GraphNeighbor(file_id=file_id, edge_type="references", weight=1, hop=hop)


# ---------------------------------------------------------------------------
# Core acceptance criteria: oversized pool truncated to k + 2k
# ---------------------------------------------------------------------------


def test_combine_and_cap_oversized_pool_truncated_to_k_plus_2k() -> None:
    k = 3
    # 3 selected topics + 2 expansions each returning 6 distinct neighbors = 15 distinct
    # files available, well over the cap of k + 2k = 9.
    selected = [_topic(1), _topic(2), _topic(3)]
    expansions = [
        ExpansionResult(topic=selected[0], neighbors=[_neighbor(fid) for fid in range(100, 106)]),
        ExpansionResult(topic=selected[1], neighbors=[_neighbor(fid) for fid in range(200, 206)]),
    ]
    available = len({t.file_id for t in selected} | {n.file_id for e in expansions for n in e.neighbors})
    assert available == 15

    result = combine_and_cap(selected, expansions, k=k)

    assert len(result) == min(available, k + 2 * k)
    assert len(result) == 9


def test_combine_and_cap_under_cap_returns_all_available() -> None:
    k = 3
    selected = [_topic(1), _topic(2)]
    expansions = [ExpansionResult(topic=selected[0], neighbors=[_neighbor(10), _neighbor(11)])]

    result = combine_and_cap(selected, expansions, k=k)

    available = 4  # 1, 2, 10, 11 -- all distinct, under the cap of 9
    assert len(result) == min(available, k + 2 * k)
    assert len(result) == 4
    assert set(result) == {1, 2, 10, 11}


# ---------------------------------------------------------------------------
# Dedup: a file reachable both as a direct selection and as someone else's
# expansion neighbor counts once toward the cap.
# ---------------------------------------------------------------------------


def test_combine_and_cap_dedups_file_reachable_both_ways() -> None:
    selected = [_topic(1), _topic(2)]
    # Topic 2's expansion neighbors happen to include file_id 1 (already directly selected).
    expansions = [ExpansionResult(topic=selected[1], neighbors=[_neighbor(1), _neighbor(99)])]

    result = combine_and_cap(selected, expansions, k=3)

    assert result.count(1) == 1
    assert set(result) == {1, 2, 99}
    assert len(result) == 3


def test_combine_and_cap_dedups_across_two_expansions() -> None:
    selected = [_topic(1), _topic(2)]
    # The same neighbor file_id (50) is returned by two different topics' expansions.
    expansions = [
        ExpansionResult(topic=selected[0], neighbors=[_neighbor(50)]),
        ExpansionResult(topic=selected[1], neighbors=[_neighbor(50), _neighbor(51)]),
    ]

    result = combine_and_cap(selected, expansions, k=3)

    assert result.count(50) == 1
    assert set(result) == {1, 2, 50, 51}
    assert len(result) == 4


# ---------------------------------------------------------------------------
# Priority: selected topics must survive truncation over expansion neighbors.
# ---------------------------------------------------------------------------


def test_combine_and_cap_prioritizes_selected_over_expansion_when_truncating() -> None:
    k = 3
    selected = [_topic(1), _topic(2), _topic(3)]
    # 20 distinct expansion neighbors -- far more than fit under the cap of 9.
    expansions = [
        ExpansionResult(topic=selected[0], neighbors=[_neighbor(fid) for fid in range(1000, 1020)]),
    ]

    result = combine_and_cap(selected, expansions, k=k)

    assert len(result) == k + 2 * k
    # All directly-selected file_ids must be present; only expansion neighbors are dropped.
    assert {1, 2, 3}.issubset(set(result))
    dropped_would_be_selected = {1, 2, 3} - set(result)
    assert dropped_would_be_selected == set()


# ---------------------------------------------------------------------------
# Ordering
# ---------------------------------------------------------------------------


def test_combine_and_cap_preserves_order() -> None:
    selected = [_topic(3), _topic(1), _topic(2)]  # deliberately not id-sorted
    expansions = [
        ExpansionResult(topic=selected[0], neighbors=[_neighbor(30), _neighbor(31)]),
        ExpansionResult(topic=selected[1], neighbors=[_neighbor(40)]),
    ]

    result = combine_and_cap(selected, expansions, k=5)

    assert result == [3, 1, 2, 30, 31, 40]


# ---------------------------------------------------------------------------
# Empty inputs / boundary k values
# ---------------------------------------------------------------------------


def test_combine_and_cap_empty_inputs_return_empty_list() -> None:
    assert combine_and_cap([], [], k=3) == []


def test_combine_and_cap_expansion_with_empty_neighbors_contributes_nothing() -> None:
    selected = [_topic(1)]
    expansions = [ExpansionResult(topic=selected[0], neighbors=[])]

    result = combine_and_cap(selected, expansions, k=3)

    assert result == [1]


def test_combine_and_cap_default_k_matches_default_k_constant() -> None:
    # Default cap is DEFAULT_K + 2 * DEFAULT_K == 9.
    selected = [_topic(fid) for fid in range(20)]

    result = combine_and_cap(selected, [])

    assert len(result) == DEFAULT_K + 2 * DEFAULT_K
    assert len(result) == 9


def test_combine_and_cap_k_zero_returns_empty_list() -> None:
    selected = [_topic(1), _topic(2)]
    expansions = [ExpansionResult(topic=selected[0], neighbors=[_neighbor(10)])]

    result = combine_and_cap(selected, expansions, k=0)

    assert result == []


@pytest.mark.parametrize("k", [-1, -5])
def test_combine_and_cap_rejects_negative_k(k: int) -> None:
    with pytest.raises(ValueError):
        combine_and_cap([], [], k=k)


def test_combine_and_cap_duplicate_file_id_within_selected_counts_once() -> None:
    # Should not occur per select_top_k's own contract, but handled safely regardless.
    selected = [_topic(1), _topic(1), _topic(2)]

    result = combine_and_cap(selected, [], k=3)

    assert result == [1, 2]


# ---------------------------------------------------------------------------
# Parametrized: the k + 2k cap formula itself, across several k values (not just
# k=3/default). Per issue #55 subtask 4.5.17.4 -- this was "manually verified correct
# during CDR verification" but never committed as a test until now.
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("k", [0, 1, 2, 5, 10])
def test_combine_and_cap_parametrized_k_matches_k_plus_2k_formula(k: int) -> None:
    # Always build an oversized (or exactly-at-cap) pool of distinct file_ids: 3 directly
    # selected topics plus enough distinct expansion neighbors that the total available count
    # is always >= k + 2*k, so the cap is the binding constraint (or matched exactly).
    cap = k + 2 * k
    num_neighbors = max(cap, 12)  # keep pools comfortably oversized even for k=10 (cap=30)

    selected = [_topic(1), _topic(2), _topic(3)]
    expansions = [
        ExpansionResult(
            topic=selected[0],
            neighbors=[_neighbor(fid) for fid in range(1000, 1000 + num_neighbors)],
        ),
    ]

    available = len({t.file_id for t in selected} | {n.file_id for e in expansions for n in e.neighbors})
    assert available >= cap  # sanity: pool is never smaller than the cap being tested

    result = combine_and_cap(selected, expansions, k=k)

    assert len(result) == cap
    assert len(result) == min(available, cap)
    assert len(set(result)) == len(result)  # no duplicates at any k
