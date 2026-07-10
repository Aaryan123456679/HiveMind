"""Integration tests composing `query.topic_selector`'s full pipeline.

Per issue #23 subtask 4.4.4's test spec: "pytest
agents/query/test_topic_selector_integration.py: single fixture scenario
exercising all three behaviors together, assert final output set and
composition." This file composes the three *real* pipeline functions --
`select_top_k`, then `expand_insufficient_topics` (which itself calls
`is_insufficient_alone` internally), then `combine_and_cap` -- in sequence,
exactly as the query pipeline would. The only test double is
`GraphNeighborsFn`, the one boundary that is actually RPC-shaped (delegating
to the Go engine's `GraphNeighbors`); everything else runs the real,
already-unit-tested (4.4.1/4.4.2/4.4.3) implementation.

Three scenarios (a small number of realistic end-to-end cases, rather than a
single fixture, per the launching dispatch's explicit guidance) cover:

1. `test_pipeline_expansion_triggers_and_cap_enforced_end_to_end` -- more
   candidates than `k`, the weakest selected topic triggers expansion, and
   the combined+capped result respects the `k + 2k` invariant end-to-end.
2. `test_pipeline_no_insufficient_topics_no_expansion_calls` -- no selected
   topic is judged insufficient, so zero `GraphNeighbors` calls happen at
   all (not merely that their results are unused).
3. `test_pipeline_dedup_across_expansion_end_to_end` -- two independently
   flagged topics' expansions share a neighbor, and one neighbor also
   collides with a directly-selected topic's own file_id; both dedup cases
   must surface through the full pipeline, not just through `combine_and_cap`
   called in isolation (as `test_topic_selector_cap.py` already covers).

Per-function unit edge cases (negative `k`/`ratio`/`hops`, empty inputs,
tie-break determinism, etc.) are already covered by
`test_topic_selector.py`, `test_topic_selector_expansion.py`, and
`test_topic_selector_cap.py` respectively -- this file only proves
*composition*, not each function's own contract in isolation.
"""

from __future__ import annotations

from query.topic_selector import (
    DEFAULT_EXPANSION_HOPS,
    GraphNeighbor,
    TopicCandidate,
    combine_and_cap,
    expand_insufficient_topics,
    select_top_k,
)


def _topic(file_id: int, score: float) -> TopicCandidate:
    return TopicCandidate(file_id=file_id, path=f"p/{file_id}", score=score)


def _neighbor(file_id: int, hop: int = 1) -> GraphNeighbor:
    return GraphNeighbor(file_id=file_id, edge_type="references", weight=1, hop=hop)


class _RecordingGraphNeighbors:
    """Plain mock `GraphNeighborsFn`: records `(file_id, hops)` calls and returns a
    canned per-file_id neighbor list. Same shape as
    `test_topic_selector_expansion.py`'s `_RecordingGraphNeighbors`, reused here so the
    integration test's mock boundary matches the convention already established (and
    verified) for 4.4.2, rather than introducing a divergent second mock shape for the
    same `GraphNeighborsFn` contract in this same package.
    """

    def __init__(self, neighbors_by_file_id: dict[int, list[GraphNeighbor]]) -> None:
        self._neighbors_by_file_id = neighbors_by_file_id
        self.calls: list[tuple[int, int]] = []

    def __call__(self, file_id: int, hops: int) -> list[GraphNeighbor]:
        self.calls.append((file_id, hops))
        return self._neighbors_by_file_id.get(file_id, [])


# ---------------------------------------------------------------------------
# Scenario 1: expansion triggers for the weakest selected topic; combined
# result respects the k + 2k hard cap end-to-end.
# ---------------------------------------------------------------------------


def test_pipeline_expansion_triggers_and_cap_enforced_end_to_end() -> None:
    k = 3

    # 5 candidates -- more than k. Top-3 by score are file_ids 1 (0.95), 2 (0.60), 3
    # (0.40); the other two (4, 5) fall outside the selection entirely.
    candidates = [
        _topic(1, 0.95),
        _topic(2, 0.60),
        _topic(3, 0.40),
        _topic(4, 0.20),
        _topic(5, 0.10),
    ]

    selected = select_top_k(candidates, k=k)

    # Real select_top_k output: exact top-3 membership and descending-score order.
    assert [t.file_id for t in selected] == [1, 2, 3]

    # Among the selection, top_score == 0.95; the insufficiency threshold (default
    # ratio 0.5) is 0.475. Topic 3 (0.40) falls below it; topic 2 (0.60) does not.
    mock = _RecordingGraphNeighbors(
        {3: [_neighbor(fid) for fid in range(100, 110)]}  # 10 distinct neighbors -- oversized
    )

    expansions = expand_insufficient_topics(selected, mock)

    # Expansion requested only for the one weak topic (file_id 3), at the default hop
    # depth -- driven by the real is_insufficient_alone logic, not asserted directly.
    assert mock.calls == [(3, DEFAULT_EXPANSION_HOPS)]
    assert [e.topic.file_id for e in expansions] == [3]

    result = combine_and_cap(selected, expansions, k=k)

    # k + 2k == 9: 3 selected + 10 available neighbors (13 distinct total) must be
    # truncated to exactly 9, with all 3 directly-selected topics surviving truncation.
    assert len(result) == k + 2 * k
    assert len(result) == 9
    assert {1, 2, 3}.issubset(set(result))


# ---------------------------------------------------------------------------
# Scenario 2: no selected topic is judged insufficient -- zero GraphNeighbors
# calls happen at all, and the capped result is exactly the selection.
# ---------------------------------------------------------------------------


def test_pipeline_no_insufficient_topics_no_expansion_calls() -> None:
    k = 3

    # All three scores are within the default ratio (0.5) of the top score (0.9);
    # threshold is 0.45, and the lowest score here (0.80) is well above it.
    candidates = [
        _topic(1, 0.90),
        _topic(2, 0.85),
        _topic(3, 0.80),
    ]

    selected = select_top_k(candidates, k=k)
    assert [t.file_id for t in selected] == [1, 2, 3]

    mock = _RecordingGraphNeighbors({})
    expansions = expand_insufficient_topics(selected, mock)

    # No topic flagged insufficient -> no GraphNeighbors RPC-shaped call at all.
    assert expansions == []
    assert mock.calls == []

    result = combine_and_cap(selected, expansions, k=k)

    # With no expansions, the final result is exactly the selected file_ids, in order,
    # well under the k + 2k cap.
    assert result == [1, 2, 3]
    assert len(result) <= k + 2 * k


# ---------------------------------------------------------------------------
# Scenario 3: two independently-insufficient topics' expansions overlap with
# each other AND with a directly-selected topic's own file_id -- both dedup
# cases must surface through the full pipeline, not just combine_and_cap.
# ---------------------------------------------------------------------------


def test_pipeline_dedup_across_expansion_end_to_end() -> None:
    k = 3

    # 4 candidates; top-3 by score are file_ids 1 (0.90), 3 (0.35), 2 (0.30) -- file_id
    # 4 (0.10) falls outside the selection.
    candidates = [
        _topic(1, 0.90),
        _topic(2, 0.30),
        _topic(3, 0.35),
        _topic(4, 0.10),
    ]

    selected = select_top_k(candidates, k=k)
    assert [t.file_id for t in selected] == [1, 3, 2]

    # top_score == 0.90; threshold 0.45. Both topic 3 (0.35) and topic 2 (0.30) fall
    # below it; topic 1 (0.90) does not.
    #
    # Topic 3's expansion returns neighbor file_id 50 (shared with topic 2's expansion
    # below -- neighbor/neighbor collision) and file_id 1 (colliding with the directly
    # selected topic 1 -- selected/neighbor collision).
    # Topic 2's expansion returns the same file_id 50, plus a unique file_id 60.
    mock = _RecordingGraphNeighbors(
        {
            3: [_neighbor(50), _neighbor(1)],
            2: [_neighbor(50), _neighbor(60)],
        }
    )

    expansions = expand_insufficient_topics(selected, mock)

    # Both insufficient topics triggered exactly one call each, in selected's own
    # order (topic 3 before topic 2), at the default hop depth.
    assert mock.calls == [(3, DEFAULT_EXPANSION_HOPS), (2, DEFAULT_EXPANSION_HOPS)]
    assert [e.topic.file_id for e in expansions] == [3, 2]

    result = combine_and_cap(selected, expansions, k=k)

    # Distinct file_ids across selected ({1, 3, 2}) and neighbors ({50, 1, 50, 60}) are
    # {1, 2, 3, 50, 60} -- 5 total, well under the k + 2k == 9 cap, so dedup (not
    # truncation) is what is being proven here.
    assert set(result) == {1, 2, 3, 50, 60}
    assert len(result) == 5
    assert len(result) < k + 2 * k

    # Neighbor/neighbor collision: file_id 50 appears exactly once despite being
    # returned by both topic 3's and topic 2's expansions.
    assert result.count(50) == 1

    # Selected/neighbor collision: file_id 1 appears exactly once despite also being
    # returned as topic 3's expansion neighbor -- the directly-selected occurrence wins.
    assert result.count(1) == 1
