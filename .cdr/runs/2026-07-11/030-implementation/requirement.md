# Requirement -- Issue #25 subtask 4.6.1

## Title
Wire query -> intent_refiner -> topic_selector -> synthesizer -> answer, exposed via api/'s
`/query` route.

## Acceptance criteria (verbatim from `gh issue view 25`)
A single pipeline function/class chains all three agents (intent_refiner, topic_selector,
synthesizer) in order and is reachable through the api/ gateway's `/query` HTTP route.

## Test spec (verbatim)
`go test ./api/... -run TestQueryRoute` (agents/query pipeline mocked at the gRPC boundary)
plus `pytest agents/query/test_pipeline.py` for the Python-side chain: assert correct call
order and response shape end-to-end.

## Impacted modules (verbatim)
`agents/query/pipeline.py`, `api/routes/query.go`

## Discovery findings that shape this implementation

1. **No gRPC wiring exists anywhere in `agents/query/` today.** `topic_selector.py`'s own
   module docstring (4.4.1/4.4.2 disclosed choices) explicitly states: "`agents/query/` has
   no gRPC client wiring yet (no `wiring.py` analogue exists in this package the way
   `agents/ingestion/` has one)" and "no real gRPC-backed implementation exists yet" for
   `GraphNeighborsFn`. `SearchCandidatesFn` / `GraphNeighborsFn` are documented injection-point
   type aliases only, never called from within `topic_selector.py` itself.
2. **`combine_and_cap()` returns bare `file_id: int` values, not paths/content.** Its own
   module comment states "Mapping `file_id`s back to file content for the synthesizer prompt
   is left to a later subtask (out of scope here)" -- i.e. this subtask (4.6.1) is that later
   subtask. `synthesize_answer()` needs `selected_markdown` with `"## File: <path>"` headers,
   so the pipeline needs a way to resolve `file_id -> (path, content)`. No such RPC client
   exists either (`GetFile`/`ReadPartial` are engine RPCs with Go server handlers but no
   Python-side client wiring in `agents/query/`).
3. **`api/` is a near-empty Go module.** `api/main.go` is literally `func main() {}` -- no
   HTTP router, no existing routes (no `/health` or similar precedent), no gRPC client to the
   Python agent service. `docs/HLD.md` describes the intended shape ("API Gateway (Go, api/)
   -> gRPC -> Go Storage Engine" and routes `/ingest /query /graph /files /admin` that "fan
   out to the engine and the agent service"), but none of this is implemented yet, and
   `proto/hivemind.proto`'s `service HiveMind` defines exactly 9 RPCs (`PutSegment`, `GetFile`,
   `ReadPartial`, `GraphNeighbors`, `SearchCandidates`, `ProposeSplit`, `PutEdge`, `PutEntity`,
   `LookupEntity`) -- **no RPC exists for invoking the Python query pipeline from Go.** Adding
   one would require extending the shared `.proto` contract and regenerating both Go and
   Python stubs, which is out of scope for a single-commit subtask sized like its siblings.

## Disclosed decision (per the dispatching agent's explicit instruction to pick and disclose)

Given the above, real gRPC/HTTP wiring end-to-end (Go `/query` -> Python query-pipeline
process -> engine `SearchCandidates`/`GraphNeighbors`/`GetFile`) is **out of scope** for this
subtask and remains a disclosed gap for a later subtask (extending `proto/hivemind.proto` with
a `RunQuery`-style RPC, or an HTTP/JSON bridge, is a reasonable follow-up shape but is not
decided here).

This subtask instead:
- Builds `agents/query/pipeline.py`: a single function (`run_query_pipeline`) that chains
  `intent_refiner.refine_intent -> topic_selector.select_top_k ->
  topic_selector.expand_insufficient_topics -> topic_selector.combine_and_cap ->
  synthesizer.synthesize_answer` in order, with `SearchCandidatesFn`, `GraphNeighborsFn`, and a
  new `GetFileFn` (file_id -> (path, content)) injected as callables -- exactly the established
  DI pattern from `topic_selector.py`/`synthesizer.py`'s own "inject a client under
  `TYPE_CHECKING`/callable-type-alias" convention. No real gRPC client is constructed inside
  `pipeline.py`.
- Builds `api/routes/query.go`: an HTTP handler for `POST /query` that depends on a small
  `QueryPipeline` Go interface (`RunQuery(ctx, query, history) (QueryResult, error)`) --  this
  is the "gRPC boundary" the test spec says is mocked. The handler is registered into a
  `net/http.ServeMux` reachable from `api/main.go`. The concrete real-wiring implementation of
  `QueryPipeline` (an actual gRPC/HTTP call into the Python process running
  `run_query_pipeline`) is **not** built in this subtask; `main.go` wires in a minimal
  `notImplementedPipeline` stand-in that returns a clear "not yet wired" error, so the route is
  reachable and structurally correct without fabricating a fake network call.

This keeps the subtask minimal, consistent with the codebase's existing DI conventions, and
matches choice (b) offered by the dispatching instructions.

## Test files to create (not present before this subtask)
- `agents/query/test_pipeline.py` (new)
- `api/routes/query_test.go` (new, alongside new `api/routes/query.go`)
