# Plan — 4.4.1 topic_selector.py

1. Create `agents/query/topic_selector.py`:
   - Module docstring: purpose, citation of issue #23/4.4.1 + LLD section,
     disclosed design choices (input shape decision; extensibility framing
     for 4.4.2/4.4.3 without building them).
   - `from __future__ import annotations`.
   - `TopicCandidate` frozen dataclass: `file_id: int`, `path: str`,
     `score: float`.
   - `SearchCandidatesFn = Callable[[str, int], Sequence[TopicCandidate]]`
     type alias (declared for forward-compat with future gRPC-wiring
     subtasks, matching `ingestion.shortlist`'s precedent; not consumed by
     `select_top_k` itself in this dispatch).
   - `DEFAULT_K = 3` module-level constant.
   - `select_top_k(candidates: Sequence[TopicCandidate], *, k: int = DEFAULT_K) -> list[TopicCandidate]`:
     - Validate `k >= 0`, raise `ValueError` otherwise (mirrors
       `shortlist()`'s bound-validation precedent).
     - Sort by descending `score`, ties broken by original input order
       (stable sort on negated score) for determinism -- matches
       `shortlist()`'s `(-scores[i], i)` tie-break precedent.
     - Truncate to `k`, return `list[TopicCandidate]`.
     - Never returns more than `min(k, len(candidates))`.
2. Create `agents/query/test_topic_selector.py`:
   - Fixture candidate list (>=5 entries, distinct/tied scores) matching
     issue's explicit "fixture candidate list" phrasing.
   - Parametrized test over k=1,3,5 per issue's exact test spec wording,
     asserting: correct length, correct top-scoring subset/order.
   - Default-k test (no `k` kwarg) asserts default is 3.
   - Edge cases: k=0 -> empty list; k > len(candidates) -> full list,
     no error; negative k -> ValueError; tie-break determinism test.
3. Run `cd agents && python3 -m pytest query/ -q`.
4. Run regression: `python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q`
   (ignoring pre-existing protobuf collection error, issue #46, already
   known/tracked, not touched by this change).
5. Run `ruff check agents/query/topic_selector.py agents/query/test_topic_selector.py`
   (and full `ruff check .` from `agents/` for regression signal).
6. Write self-consistency.json (build green + validation-matrix coverage
   check only -- NOT verification per I4).
7. One local commit (Problem/Solution/Impact style), no push.
8. Write handoff.json with pointers only.
