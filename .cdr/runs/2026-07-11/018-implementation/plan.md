# Plan — 4.4.2

1. Extend `agents/query/topic_selector.py` (append after `select_top_k`, do not touch
   existing code):
   - `DEFAULT_INSUFFICIENCY_RATIO = 0.5` constant.
   - `DEFAULT_EXPANSION_HOPS = 2` constant.
   - `GraphNeighbor` frozen dataclass (`file_id: int`, `edge_type: str`, `weight: int`,
     `hop: int`) mirroring proto `Neighbor`.
   - `GraphNeighborsFn = Callable[[int, int], Sequence[GraphNeighbor]]` type alias,
     called as `(file_id, hops)`.
   - `is_insufficient_alone(topic, top_score, *, ratio=DEFAULT_INSUFFICIENCY_RATIO) -> bool`
     helper: `topic.score < ratio * top_score`.
   - `ExpansionResult` frozen dataclass (`topic: TopicCandidate`, `neighbors:
     list[GraphNeighbor]`).
   - `expand_insufficient_topics(selected, graph_neighbors, *, hops=DEFAULT_EXPANSION_HOPS,
     ratio=DEFAULT_INSUFFICIENCY_RATIO) -> list[ExpansionResult]`:
     - Validate `0 <= hops <= 2` (raise `ValueError` otherwise, mirroring engine guard).
     - Validate `0 <= ratio` (raise `ValueError` if negative).
     - If `selected` empty, return `[]` (no RPC calls).
     - `top_score = max(t.score for t in selected)`.
     - For each topic in `selected` (original order), if `is_insufficient_alone(topic,
       top_score, ratio=ratio)`, call `graph_neighbors(topic.file_id, hops)` and append
       an `ExpansionResult`. Topics not flagged: no call at all (test spec: "requested
       only for topics flagged insufficient").
2. Create `agents/query/test_topic_selector_expansion.py`:
   - Fixture candidates with a clear top score and 1-2 clearly-insufficient scores
     (below ratio threshold) and possibly one borderline-sufficient score.
   - Mock `graph_neighbors` as a `Mock()` or list-recording plain function.
   - Assert: called exactly once per flagged topic, with `(file_id, DEFAULT_EXPANSION_HOPS)`
     args; NOT called for the top/sufficient topic; returned `ExpansionResult` list
     has exactly the flagged topics, in original order, with correct `neighbors` payload
     passed through unmodified.
   - Test custom `hops` param is forwarded to the mock.
   - Test `ValueError` for `hops` out of `[0,2]` and negative `ratio`.
   - Test empty `selected` list -> `[]`, mock never called.
   - Test `is_insufficient_alone` directly for a couple of boundary cases (`==` ratio
     threshold is NOT flagged, i.e. `<` strict).
3. Run `cd agents && python3 -m pytest query/ -q`.
4. Run full regression: `python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q`.
5. `ruff check agents/query/topic_selector.py agents/query/test_topic_selector_expansion.py`
   (or repo-configured ruff target).
6. Write self-consistency.json, commit, handoff.json.
