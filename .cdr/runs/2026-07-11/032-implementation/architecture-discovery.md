# Architecture discovery -- Issue #25 subtask 4.6.2

## Index-first pass
- `.cdr/index/task.jsonl` task-4.6.1: confirms `run_query_pipeline()` in
  `agents/query/pipeline.py` chains `refine_intent -> select_top_k ->
  expand_insufficient_topics -> combine_and_cap -> synthesize_answer`, DI seam only, no
  real gRPC wiring in `agents/query/`. Two non-blocking findings carried: F-4.6.1-1
  (`/query` returns 500 pending real wiring, Go-side, irrelevant here), F-4.6.1-2
  (`GetFileFn`'s `(path, content)` shape has no direct `GetFileResponse` proto
  counterpart -- irrelevant here since this test supplies its own fake `get_file`).
- `.cdr/index/file.jsonl`: `docs/LLD/query-agent.md` is the only indexed LLD doc for
  `agents/query`; read below.
- No dedicated e2e-test-pattern entry in the index; used `agents/ingestion/
  test_e2e_smoke.py` (found via `find`) as the existing e2e-naming/skip-pattern
  precedent instead.

## LLD read
`docs/LLD/query-agent.md`'s pipeline-order section: "query -> intent_refiner ->
topic_selector (+ SearchCandidates / GraphNeighbors) -> synthesizer -> answer" --
matches `pipeline.py`'s own docstring paraphrase already read in the prior run's
handoff; no new constraint beyond what 4.6.1 already captured.

## Touched-file signatures read directly (source, after indexes exhausted)
- `agents/query/pipeline.py`: `run_query_pipeline(query, history, *, llm_client,
  search_candidates, graph_neighbors, get_file, k=DEFAULT_K,
  max_candidates=DEFAULT_MAX_CANDIDATES, hops=DEFAULT_EXPANSION_HOPS,
  ratio=DEFAULT_INSUFFICIENCY_RATIO, model=None, temperature=0.0, max_tokens=None,
  timeout=None) -> QueryPipelineResult`. `PipelineError` raised on empty
  `combine_and_cap()` result. `GetFileFn = Callable[[int], tuple[str, str]]`
  (`file_id -> (path, content)`).
- `agents/query/topic_selector.py`: `TopicCandidate(file_id: int, path: str, score:
  float)` frozen dataclass; `GraphNeighbor(file_id: int, edge_type: str, weight: int,
  hop: int)`; `SearchCandidatesFn = Callable[[str, int], Sequence[TopicCandidate]]`;
  `GraphNeighborsFn = Callable[[int, int], Sequence[GraphNeighbor]]`;
  `DEFAULT_K = 3`, `DEFAULT_INSUFFICIENCY_RATIO = 0.5`, `DEFAULT_EXPANSION_HOPS = 2`.
  `is_insufficient_alone(topic, top_score, ratio)`: `topic.score < ratio * top_score`
  (strict `<`) -- if all seeded candidates' scores are within the ratio band of the top
  score, `expand_insufficient_topics` calls `graph_neighbors` zero times; used to keep
  this e2e test's fixture deterministic (no expansion-path branching needed, since
  4.6.1's own `test_pipeline.py` already covers the expansion-call-order case
  end-to-end at the unit level).
- `agents/query/synthesizer.py`: `SynthesizerResult(answer: str, citations: list[str],
  provided_paths: list[str])`, `.unknown_citations()` returns the order-preserved,
  deduplicated subset of `citations` not present in `provided_paths`. File-path header
  format is `"## File: <path>"` (regex `^##\s*File:\s*(?P<path>.+?)\s*$`,
  MULTILINE) -- `_build_selected_markdown` in `pipeline.py` emits exactly this format,
  so `provided_paths` on the real `SynthesizerResult` will contain exactly the seeded
  corpus paths this test's `get_file` resolves.
- `agents/llm/client.py`: `LLMClient` is an `abc.ABC` with one abstract method,
  `complete(prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None) ->
  str`. Test fakes subclass this directly (matching `test_pipeline.py`'s own
  `_FakeLLMClient` convention) rather than a `Protocol`/mock, per this module's own
  disclosed ABC-over-Protocol design choice.
- `agents/query/intent_refiner.py`: `refine_intent(query, history, llm_client, ...) ->
  IntentRefinerResult(refined_intent: str, entities: list[str], query_type: QueryType)`.

## Existing e2e-test convention read (`agents/ingestion/test_e2e_smoke.py`)
Real subprocess engine + real Ollama, skipped (not failed) via `pytest.mark.skipif` when
prerequisites (go toolchain / grpc stubs / reachable Ollama) are absent. This subtask's
own scope (per requirement.md's "what end-to-end means" section) does NOT require a real
engine subprocess or real Ollama -- 4.6.1's pipeline has no gRPC wiring to stand a real
engine up against, and the dispatching instructions are explicit that this test injects
"minimal-but-real fake implementations... backed by an actual small in-memory or
on-disk seeded corpus", not a real network/gRPC boundary. Adopted instead: real files
written to `tmp_path` (pytest's own fixture, not a hand-rolled temp-dir helper), read
back genuinely from disk by the fake `get_file`/`search_candidates` callables (no
in-memory dict standing in for file content) -- this is the "real" part of this
e2e test, and the LLM/gRPC DI seam is the (disclosed, accepted) fake part.
