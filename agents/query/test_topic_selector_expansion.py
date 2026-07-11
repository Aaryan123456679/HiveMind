"""Tests for `query.topic_selector.expand_insufficient_topics` /
`is_insufficient_alone`.

Per issue #23 subtask 4.4.2's test spec: "pytest
agents/query/test_topic_selector_expansion.py (GraphNeighbors mocked): assert
expansion is requested only for topics flagged insufficient, with correct
hop-depth parameter."
"""

from __future__ import annotations

import pytest

from query.topic_selector import (
    DEFAULT_EXPANSION_HOPS,
    DEFAULT_INSUFFICIENCY_RATIO,
    GraphNeighbor,
    TopicCandidate,
    expand_insufficient_topics,
    is_insufficient_alone,
)

# ---------------------------------------------------------------------------
# Fixture: a selection with one clearly-sufficient (top) topic and two
# clearly-insufficient ones (score well below DEFAULT_INSUFFICIENCY_RATIO
# (0.5) of the top score).
# ---------------------------------------------------------------------------

_TOP = TopicCandidate(file_id=3, path="billing/PaymentDelays", score=0.90)
_INSUFFICIENT_A = TopicCandidate(file_id=1, path="billing/InvoiceDisputes", score=0.30)
_INSUFFICIENT_B = TopicCandidate(file_id=6, path="engineering/IncidentPostmortems", score=0.10)

_SELECTED = [_TOP, _INSUFFICIENT_A, _INSUFFICIENT_B]

_NEIGHBOR_FOR_A = GraphNeighbor(file_id=101, edge_type="LLM_ASSERTED", weight=2, hop=1)
_NEIGHBOR_FOR_B = GraphNeighbor(file_id=102, edge_type="ENTITY_COOCCUR", weight=5, hop=2)


class _RecordingGraphNeighbors:
    """Plain mock `GraphNeighborsFn`: records every call's args and returns a
    per-file_id canned neighbor list."""

    def __init__(self, neighbors_by_file_id: dict[int, list[GraphNeighbor]]) -> None:
        self._neighbors_by_file_id = neighbors_by_file_id
        self.calls: list[tuple[int, int]] = []

    def __call__(self, file_id: int, hops: int) -> list[GraphNeighbor]:
        self.calls.append((file_id, hops))
        return self._neighbors_by_file_id.get(file_id, [])


# ---------------------------------------------------------------------------
# Core acceptance criteria: expansion requested only for flagged topics, with
# the correct hop-depth parameter.
# ---------------------------------------------------------------------------


def test_expand_calls_only_for_insufficient_topics() -> None:
    mock = _RecordingGraphNeighbors(
        {
            _INSUFFICIENT_A.file_id: [_NEIGHBOR_FOR_A],
            _INSUFFICIENT_B.file_id: [_NEIGHBOR_FOR_B],
        }
    )

    results = expand_insufficient_topics(_SELECTED, mock)

    # Only the two insufficient topics triggered a call; the top topic did not.
    assert mock.calls == [
        (_INSUFFICIENT_A.file_id, DEFAULT_EXPANSION_HOPS),
        (_INSUFFICIENT_B.file_id, DEFAULT_EXPANSION_HOPS),
    ]
    assert [r.topic.file_id for r in results] == [
        _INSUFFICIENT_A.file_id,
        _INSUFFICIENT_B.file_id,
    ]
    assert results[0].neighbors == [_NEIGHBOR_FOR_A]
    assert results[1].neighbors == [_NEIGHBOR_FOR_B]


def test_expand_uses_default_hops() -> None:
    mock = _RecordingGraphNeighbors({})

    expand_insufficient_topics(_SELECTED, mock)

    assert DEFAULT_EXPANSION_HOPS == 2
    assert all(hops == DEFAULT_EXPANSION_HOPS for _file_id, hops in mock.calls)


def test_expand_custom_hops_forwarded() -> None:
    mock = _RecordingGraphNeighbors({})

    expand_insufficient_topics(_SELECTED, mock, hops=0)

    assert mock.calls == [
        (_INSUFFICIENT_A.file_id, 0),
        (_INSUFFICIENT_B.file_id, 0),
    ]


def test_expand_all_sufficient_no_calls() -> None:
    all_sufficient = [
        TopicCandidate(file_id=1, path="a/A", score=0.9),
        TopicCandidate(file_id=2, path="b/B", score=0.8),
    ]
    mock = _RecordingGraphNeighbors({})

    results = expand_insufficient_topics(all_sufficient, mock)

    assert results == []
    assert mock.calls == []


def test_expand_empty_selected_no_calls() -> None:
    mock = _RecordingGraphNeighbors({})

    results = expand_insufficient_topics([], mock)

    assert results == []
    assert mock.calls == []


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("hops", [-1, 3, 100])
def test_expand_rejects_hops_out_of_range(hops: int) -> None:
    mock = _RecordingGraphNeighbors({})

    with pytest.raises(ValueError):
        expand_insufficient_topics(_SELECTED, mock, hops=hops)

    assert mock.calls == []


def test_expand_rejects_negative_ratio() -> None:
    mock = _RecordingGraphNeighbors({})

    with pytest.raises(ValueError):
        expand_insufficient_topics(_SELECTED, mock, ratio=-0.1)


# ---------------------------------------------------------------------------
# `is_insufficient_alone` boundary behavior
# ---------------------------------------------------------------------------


def test_is_insufficient_alone_below_threshold_is_flagged() -> None:
    topic = TopicCandidate(file_id=1, path="a/A", score=0.4)

    assert is_insufficient_alone(topic, top_score=1.0, ratio=0.5) is True


def test_is_insufficient_alone_at_threshold_is_not_flagged() -> None:
    topic = TopicCandidate(file_id=1, path="a/A", score=0.5)

    # Exactly at ratio * top_score -- strict `<` means this is NOT flagged.
    assert is_insufficient_alone(topic, top_score=1.0, ratio=0.5) is False


def test_is_insufficient_alone_top_topic_never_flagged() -> None:
    top_score = 0.9
    topic = TopicCandidate(file_id=1, path="a/A", score=top_score)

    assert is_insufficient_alone(topic, top_score=top_score) is False


def test_is_insufficient_alone_default_ratio_is_one_half() -> None:
    assert DEFAULT_INSUFFICIENCY_RATIO == 0.5


def test_is_insufficient_alone_rejects_negative_ratio() -> None:
    topic = TopicCandidate(file_id=1, path="a/A", score=0.1)

    with pytest.raises(ValueError):
        is_insufficient_alone(topic, top_score=1.0, ratio=-0.1)


# ---------------------------------------------------------------------------
# `is_insufficient_alone` negative/zero `top_score` hardening
#
# Subtask 4.5.17.3: `topic.score < ratio * top_score` would previously flag the top
# topic itself (`topic.score == top_score`) whenever `top_score` was negative, since
# multiplying a negative number by a fraction in `[0, 1]` moves the threshold *up*
# rather than down -- violating the "top topic is never flagged" invariant this
# section's tests already assert for positive `top_score`. The fix conservatively
# never flags anything when `top_score <= 0`.
# ---------------------------------------------------------------------------


def test_is_insufficient_alone_negative_score_top_topic_never_flagged() -> None:
    top_score = -5.0
    topic = TopicCandidate(file_id=1, path="a/A", score=top_score)

    assert is_insufficient_alone(topic, top_score=top_score) is False


def test_is_insufficient_alone_negative_score_never_flags_lower_score_topic() -> (
    None
):
    # Even a topic scoring well below the (negative) top_score is not flagged: once
    # top_score <= 0, "a fraction of top_score" is not a well-defined sufficiency
    # floor, so is_insufficient_alone conservatively never flags anything.
    topic = TopicCandidate(file_id=1, path="a/A", score=-50.0)

    assert is_insufficient_alone(topic, top_score=-5.0, ratio=0.5) is False


def test_is_insufficient_alone_zero_top_score_never_flagged() -> None:
    topic = TopicCandidate(file_id=1, path="a/A", score=0.0)

    assert is_insufficient_alone(topic, top_score=0.0) is False
