"""Shared test fixtures/helpers for `agents/query/`'s test suite.

Extracted per issue #55 subtask 4.5.17.5: `_FakeLLMClient` was previously duplicated
near-verbatim between `test_intent_refiner.py` and `test_intent_refiner_types.py`, and
`_RecordingGraphNeighbors` (plus its `_topic`/`_neighbor` fixture-builder companions) was
duplicated across `test_topic_selector_expansion.py`, `test_topic_selector_cap.py`, and
`test_topic_selector_integration.py`. Consolidated here as a single source of truth --
no behavioral change from any of the original per-file copies.
"""

from __future__ import annotations

from llm.client import LLMClient
from query.topic_selector import GraphNeighbor, TopicCandidate


class FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string.

    Mirrors `ingestion.test_segment._FakeLLMClient`: captures the prompt/kwargs it was
    called with, for assertions, and is a real ABC subclass (not `MagicMock(spec=LLMClient)`)
    for straightforward ABC compliance. Supports both a canned `response` string and an
    optional `error` to raise instead, covering both call sites' original needs
    (`test_intent_refiner.py` used the `error` param; `test_intent_refiner_types.py` did not).
    """

    def __init__(self, response: str | None = None, error: Exception | None = None) -> None:
        self.response = response
        self.error = error
        self.calls: list[dict] = []

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        self.calls.append(
            {
                "prompt": prompt,
                "model": model,
                "temperature": temperature,
                "max_tokens": max_tokens,
                "timeout": timeout,
            }
        )
        if self.error is not None:
            raise self.error
        assert self.response is not None
        return self.response


def topic(file_id: int, score: float = 1.0) -> TopicCandidate:
    """Build a `TopicCandidate` fixture, defaulting `path` to a stable `p/{file_id}`."""
    return TopicCandidate(file_id=file_id, path=f"p/{file_id}", score=score)


def neighbor(file_id: int, hop: int = 1) -> GraphNeighbor:
    """Build a `GraphNeighbor` fixture, defaulting `edge_type` to `"references"`."""
    return GraphNeighbor(file_id=file_id, edge_type="references", weight=1, hop=hop)


class RecordingGraphNeighbors:
    """Plain mock `GraphNeighborsFn`: records calls and returns a per-file_id canned
    neighbor list.

    Previously duplicated verbatim between `test_topic_selector_expansion.py` and
    `test_topic_selector_integration.py`.
    """

    def __init__(self, neighbors_by_file_id: dict[int, list[GraphNeighbor]]) -> None:
        self._neighbors_by_file_id = neighbors_by_file_id
        self.calls: list[tuple[int, int]] = []

    def __call__(self, file_id: int, hops: int) -> list[GraphNeighbor]:
        self.calls.append((file_id, hops))
        return self._neighbors_by_file_id.get(file_id, [])
