# Architecture discovery

## LLD (`docs/LLD/query-agent.md`)
`topic_selector.py` section: "For any topic it judges insufficient alone, it may also
request graph-traversal expansion (0-2 hops); the Go engine performs the expansion via
`GraphNeighbors`." **No concrete "insufficient alone" heuristic or hop-depth-selection
rule is specified anywhere in the LLD** — confirmed by reading the full file; this is a
disclosed-choice gap, same situation 4.4.1 encountered for "SearchCandidates result"
shape.

## Proto (`proto/hivemind.proto`)
```
message GraphNeighborsRequest {
  uint64 file_id = 1;
  int32 depth = 2;        // depth must be in [0, 2]
  EdgeType edge_type_filter = 3;
  int32 max_nodes = 4;
}
message Neighbor {
  uint64 target_file_id = 1;
  EdgeType type = 2;
  uint32 weight = 3;
  int32 hop = 4;
}
message GraphNeighborsResponse {
  repeated Neighbor neighbors = 1;
}
```

## Engine handler (`engine/rpc/server.go:276`)
`Server.GraphNeighbors` validates `depth` is in `[0, 2]` (`InvalidArgument` otherwise),
validates `max_nodes >= 0`, delegates to `graph.GraphNeighbors(s.g, fileID, depth,
edgeTypeFilter, maxNodes)`. Confirms "0-2 hop" in the issue text is literally the
engine's own valid depth range, not a per-call variable signal from elsewhere.

## No gRPC wiring in `agents/query/` (confirmed, matches 4.4.1's finding)
No `agents/query/wiring.py` exists. `agents/ingestion/shortlist.py` (3.4.2) and
`agents/ingestion/wiring.py` (3.4.4) establish the repo's DI precedent for this exact
situation: a module-level `Callable` type alias (`SearchCandidatesFn = Callable[[str,
int], Sequence[TopicCandidate]]`) is the injection point tests mock directly; a real
`Grpc*Client` class (lazy-imported `grpc`/generated stubs, `sys.path` fallback) is the
production implementation, added later or in the same subtask when in scope. This
subtask's test spec explicitly says "GraphNeighbors mocked" — same shape.

## 4.4.1's existing file (`agents/query/topic_selector.py`, commit `5cc0ea3`)
Already defines `DEFAULT_K`, `TopicCandidate` (frozen dataclass: `file_id`, `path`,
`score`), `SearchCandidatesFn` (declared, unused), `select_top_k()`. Its own docstring
explicitly anticipates 4.4.2 composing on top of `select_top_k()`'s output via a loop
deciding per-topic expansion — confirms this subtask should add free functions/types
alongside these, not modify `select_top_k` or `TopicCandidate`.

## Decisions made (disclosed, since LLD leaves them open)

1. **"Judged insufficient alone" heuristic**: score-relative-to-top-of-selection, not
   absolute. A selected topic is flagged insufficient if
   `topic.score < ratio * top_selected_score`, where `top_selected_score` is the max
   score among the topics `select_top_k` actually returned, and `ratio` is a tunable
   constant (`DEFAULT_INSUFFICIENCY_RATIO = 0.5`), not a literal. Rationale: scale of
   `score` is caller-defined (per 4.4.1, an already-decoded `SearchCandidates` result —
   could be any relevance metric), so a *relative* threshold is scale-invariant and
   avoids inventing a fixed absolute cutoff that would only be correct for one score
   distribution. This also guarantees the single best-selected topic is never flagged
   (its own score never falls below `ratio * itself` for `ratio <= 1`), matching the
   intuitive reading that the top result is presumed the most self-sufficient. Kept as
   a plain rule (multiply + compare), not an LLM call, per the dispatcher's explicit
   instruction.
2. **Hop depth**: always request `DEFAULT_EXPANSION_HOPS = 2` for every flagged topic.
   Rationale: the issue's "0-2 hop" phrasing is the *valid range the engine's RPC
   accepts* (confirmed above from `server.go`), not a described per-call variable
   selection rule — the LLD gives no signal for choosing 0 vs. 1 vs. 2 dynamically per
   topic, and inventing one would be unfounded speculation beyond this subtask's scope.
   Always requesting the max of the valid range maximizes recall for a topic already
   flagged as insufficient. `hops` is still a caller-overridable, validated (`0<=hops<=2`,
   mirroring the engine's own guard) keyword parameter, not a hardcoded literal, so a
   future caller/tunable config can override it without a signature change.
3. **DI shape**: `GraphNeighborsFn = Callable[[int, int], Sequence[GraphNeighbor]]`,
   called as `graph_neighbors(file_id, hops)`. Mirrors `SearchCandidatesFn`'s exact
   precedent shape (plain callable type alias, tests inject a mock, no real
   `Grpc*Client` built in this dispatch since it's not named in this subtask's impacted
   modules — same "gap, not this subtask's job" disclosure 4.4.1 made for
   `SearchCandidatesFn`). A new frozen `GraphNeighbor` dataclass mirrors the proto
   `Neighbor` message's fields (`file_id`, `edge_type`, `weight`, `hop`) so callers/tests
   don't need `grpc` or generated stubs, matching `TopicCandidate`'s own precedent.
