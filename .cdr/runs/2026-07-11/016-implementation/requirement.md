# Requirement — Subtask 4.4.1 (issue #23, milestone #6 "Phase 4: Query pipeline")

## Source
- GitHub issue #23 ("[4] topic_selector.py (agents/query/)"), subtask 4.4.1 of 4.
- `docs/LLD/query-agent.md` — `topic_selector.py` section.

## In-scope acceptance criteria (4.4.1 only)
> Given a `SearchCandidates` result, the selector picks the top-`k` topics by
> relevance, where `k` is a configurable parameter defaulting to 3.

## Test spec (from issue)
> `pytest agents/query/test_topic_selector.py`: assert top-k selection
> correctness for k=1,3,5 against a fixture candidate list.

## Impacted modules
- `agents/query/topic_selector.py` (new)
- `agents/query/test_topic_selector.py` (new)

## Explicitly out of scope for this dispatch
Per the dispatching instructions, subtasks 4.4.2 (graph-traversal expansion via
`GraphNeighbors`), 4.4.3 (hard-cap `k + 2k`), and 4.4.4 (integration test) are
**not** implemented now. However, since all four subtasks share the same file
(`topic_selector.py`), the shape of 4.4.1's public function/class must be
reasonably extensible for the later additions (per issue #23's own framing),
without building any of that unimplemented behavior now (no dead
branches/flags for expansion or capping).

## Key design question (resolved below, see architecture-discovery.md)
What does "a `SearchCandidates` result" look like as this function's Python
input? Resolved: mirror the precedent in `agents/ingestion/shortlist.py`
(task 3.4.2) — decouple from any gRPC-generated type; accept a plain,
frozen dataclass (`TopicCandidate`: `file_id`, `path`, `score`) and a small
injected callable Protocol (`SearchCandidatesFn`) rather than a real
`SearchCandidatesResponse`/gRPC stub. This keeps `topic_selector.py`
independently unit-testable per the test spec ("fixture candidate list")
without requiring `grpc` to be installed or a real/mocked stub tree.
