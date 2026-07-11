# Plan — subtask 4.5.17.5

1. Run baseline `pytest agents/query/ -q` and record pass count.
2. Create `agents/query/conftest.py` with:
   - `FakeLLMClient` (superset of both existing `_FakeLLMClient` variants: `response` +
     optional `error`).
   - `topic(file_id, score=1.0) -> TopicCandidate` and `neighbor(file_id, hop=1) ->
     GraphNeighbor` fixture builders.
   - `RecordingGraphNeighbors` (GraphNeighborsFn test double).
3. Update `test_intent_refiner.py` / `test_intent_refiner_types.py`: delete local
   `_FakeLLMClient`, import shared `FakeLLMClient as _FakeLLMClient`.
4. Update `test_topic_selector_expansion.py`: delete local `_RecordingGraphNeighbors`,
   import shared `RecordingGraphNeighbors as _RecordingGraphNeighbors`.
5. Update `test_topic_selector_cap.py`: delete local `_topic`/`_neighbor`, import shared
   `topic as _topic`, `neighbor as _neighbor`.
6. Update `test_topic_selector_integration.py`: delete local `_topic`/`_neighbor`/
   `_RecordingGraphNeighbors`, import all three shared, aliased.
7. Re-run `pytest agents/query/ -q`; confirm identical pass count (103) and no new
   failures/skips.
8. Run `ruff check query/` to confirm no unused-import/lint regressions.
9. Self-consistency check (build green + validation matrix covered) — NOT verification.
10. One local commit (Problem/Solution/Impact), no push.
11. Write handoff.json (pointers only).

## Explicitly out of scope
- 4.5.17.1 (live classification test), 4.5.17.2 (fences relocation), 4.5.17.3 (negative-
  score test), 4.5.17.4 (parametrized cap test, already landed at commit `5ecfcd7c`) — not
  touched by this subtask.
