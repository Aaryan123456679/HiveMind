# Plan -- issue #25 subtask 4.6.1

1. `agents/query/pipeline.py`
   - `GetFileFn = Callable[[int], tuple[str, str]]` (file_id -> (path, content)) -- new
     injection-point type alias, mirroring `SearchCandidatesFn`/`GraphNeighborsFn`'s own
     convention, documented as a disclosed choice (no real `GetFile`/`ReadPartial` client
     wiring built here).
   - `_build_selected_markdown(file_ids: Sequence[int], get_file: GetFileFn) -> str`: resolves
     each file_id to `(path, content)` and renders `"## File: {path}\n{content}\n"` blocks
     joined, matching `synthesizer.py`'s expected header format exactly.
   - `PipelineError(Exception)`: raised if `combine_and_cap` yields an empty file_id list (no
     usable context to synthesize from) -- fails fast with a clear message rather than calling
     the synthesizer with an empty `selected_markdown`.
   - `QueryPipelineResult` frozen dataclass: `intent: IntentRefinerResult`,
     `selected_file_ids: list[int]`, `synthesis: SynthesizerResult`.
   - `run_query_pipeline(query, history, *, llm_client, search_candidates, graph_neighbors,
     get_file, k=DEFAULT_K, max_candidates=DEFAULT_MAX_CANDIDATES, hops=DEFAULT_EXPANSION_HOPS,
     ratio=DEFAULT_INSUFFICIENCY_RATIO, model=None, temperature=0.0, max_tokens=None,
     timeout=None) -> QueryPipelineResult`: calls, in order,
     `refine_intent -> search_candidates -> select_top_k -> expand_insufficient_topics ->
     combine_and_cap -> _build_selected_markdown -> synthesize_answer`.

2. `agents/query/test_pipeline.py`
   - Fake `LLMClient` subclass (mirrors `test_synthesizer.py`'s `_FakeLLMClient`) returning
     canned JSON for the two LLM calls (intent-refine, synthesis) in sequence.
   - Fake `search_candidates`/`graph_neighbors`/`get_file` callables that record call order and
     arguments into a shared list.
   - Test asserts: call order is
     `[refine_intent's llm call, search_candidates, graph_neighbors (for insufficient topics
     only), get_file (once per selected file_id), synthesize's llm call]`; response shape
     (`QueryPipelineResult.synthesis.answer`/`.citations`, `.selected_file_ids`) is correct
     end-to-end for a representative fixture; empty-candidates edge case raises `PipelineError`.

3. `api/routes/query.go`
   - `QueryRequest{Query string; History []string}`, `QueryResult{Answer string; Citations
     []string}`.
   - `QueryPipeline` interface: `RunQuery(ctx context.Context, query string, history []string)
     (QueryResult, error)` -- the "gRPC boundary" mocked by the test spec.
   - `NewQueryHandler(pipeline QueryPipeline) http.HandlerFunc`: POST-only, JSON body decode,
     empty-query validation (400), delegates to `pipeline.RunQuery`, JSON-encodes
     `QueryResult` (200) or propagates error (500).
   - `RegisterRoutes(mux *http.ServeMux, pipeline QueryPipeline)`: registers `/query`.

4. `api/routes/query_test.go`
   - `TestQueryRoute`: fake `QueryPipeline` implementation records the call and returns a fixed
     `QueryResult`; asserts POST with valid JSON body returns 200 + expected JSON; asserts
     empty query returns 400; asserts pipeline error returns 500; asserts GET returns 405.

5. `api/main.go`
   - Add a minimal `notImplementedPipeline` type implementing `routes.QueryPipeline`,
     returning an explicit "query pipeline gRPC wiring not yet implemented (see issue #25
     disclosure)" error -- keeps `/query` structurally and HTTP-reachable per the acceptance
     criteria ("reachable through the api/ gateway's /query HTTP route") without fabricating a
     real network call to the Python side. `main()` registers `routes.RegisterRoutes` on an
     `http.ServeMux` and starts a real `http.ListenAndServe` on a `PORT` env var (default
     `8080`) -- the minimal amount of server-lifecycle code needed for "/query" to be an
     actually-reachable HTTP route, without inventing speculative config plumbing (auth, rate
     limiting, other routes) that later subtasks in this issue's parent epic own.

6. Self-consistency: run both test specs plus full existing suites (Python + Go) to check for
   regressions before committing.
