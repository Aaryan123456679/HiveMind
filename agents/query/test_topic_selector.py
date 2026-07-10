"""Tests for `query.topic_selector.select_top_k`.

Per issue #23 subtask 4.4.1's test spec: "pytest agents/query/test_topic_selector.py:
assert top-k selection correctness for k=1,3,5 against a fixture candidate
list." Covers the parametrized k=1,3,5 cases explicitly, plus the default-k,
relevance-ordering, and boundary/validation behavior needed to pin down
`select_top_k`'s contract for the later 4.4.2/4.4.3 subtasks that build on
top of it.
"""

from __future__ import annotations

import pytest

from query.topic_selector import DEFAULT_K, TopicCandidate, select_top_k

# ---------------------------------------------------------------------------
# Fixture candidate list
# ---------------------------------------------------------------------------

#: Six candidates with distinct scores, deliberately not pre-sorted in the
#: list itself, so a correct implementation must actually re-rank by score
#: rather than relying on input order.
_FIXTURE_CANDIDATES = [
    TopicCandidate(file_id=1, path="billing/InvoiceDisputes", score=0.42),
    TopicCandidate(file_id=2, path="hr/Onboarding", score=0.05),
    TopicCandidate(file_id=3, path="billing/PaymentDelays", score=0.91),
    TopicCandidate(file_id=4, path="legal/NDATemplates", score=0.10),
    TopicCandidate(file_id=5, path="billing/RefundRequests", score=0.77),
    TopicCandidate(file_id=6, path="engineering/IncidentPostmortems", score=0.30),
]

#: `_FIXTURE_CANDIDATES`' file_ids in expected descending-score order.
_EXPECTED_ORDER_FILE_IDS = [3, 5, 1, 6, 4, 2]


# ---------------------------------------------------------------------------
# Core acceptance criteria: top-k correctness for k=1,3,5
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("k", [1, 3, 5])
def test_select_top_k_correctness(k: int) -> None:
    result = select_top_k(_FIXTURE_CANDIDATES, k=k)

    assert len(result) == k
    assert [c.file_id for c in result] == _EXPECTED_ORDER_FILE_IDS[:k]
    # Result is sorted by descending score.
    assert [c.score for c in result] == sorted(
        (c.score for c in result), reverse=True
    )


def test_select_top_k_orders_by_descending_score() -> None:
    result = select_top_k(_FIXTURE_CANDIDATES, k=len(_FIXTURE_CANDIDATES))

    assert [c.file_id for c in result] == _EXPECTED_ORDER_FILE_IDS


# ---------------------------------------------------------------------------
# Default k
# ---------------------------------------------------------------------------


def test_select_top_k_default_k_is_three() -> None:
    assert DEFAULT_K == 3

    result = select_top_k(_FIXTURE_CANDIDATES)

    assert len(result) == 3
    assert [c.file_id for c in result] == _EXPECTED_ORDER_FILE_IDS[:3]


# ---------------------------------------------------------------------------
# Boundaries
# ---------------------------------------------------------------------------


def test_select_top_k_zero_returns_empty() -> None:
    assert select_top_k(_FIXTURE_CANDIDATES, k=0) == []


def test_select_top_k_larger_than_pool_returns_all() -> None:
    result = select_top_k(_FIXTURE_CANDIDATES, k=1000)

    assert len(result) == len(_FIXTURE_CANDIDATES)
    assert [c.file_id for c in result] == _EXPECTED_ORDER_FILE_IDS


def test_select_top_k_empty_candidates_returns_empty() -> None:
    assert select_top_k([], k=5) == []


def test_select_top_k_rejects_negative_k() -> None:
    with pytest.raises(ValueError):
        select_top_k(_FIXTURE_CANDIDATES, k=-1)


# ---------------------------------------------------------------------------
# Tie-break determinism
# ---------------------------------------------------------------------------


def test_select_top_k_tie_break_is_stable_original_order() -> None:
    tied = [
        TopicCandidate(file_id=10, path="a/A", score=0.5),
        TopicCandidate(file_id=11, path="b/B", score=0.9),
        TopicCandidate(file_id=12, path="c/C", score=0.5),
        TopicCandidate(file_id=13, path="d/D", score=0.5),
    ]

    result = select_top_k(tied, k=len(tied))

    # file_id 11 (score 0.9) ranks first; the three 0.5-scored entries keep
    # their original relative order (10, 12, 13).
    assert [c.file_id for c in result] == [11, 10, 12, 13]
