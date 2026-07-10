# Architecture discovery -- issue #25 subtask 4.6.1

## Index-first order followed
1. `.cdr/index/*.jsonl` (task/feature/decision/file/regression indexes) -- consulted first.
2. `.cdr/memory/pending.md` -- checked for prior disclosed gaps (SearchCandidates prefix-scan
   limitation; no `agents/query/wiring.py`; no gRPC client wiring).
3. Prior run handoffs: `.cdr/runs/2026-07-11/027-implementation/handoff.json` (issue #24,
   4.5.2, most recent prior run in this issue chain).
4. Targeted LLD: `docs/HLD.md` (section 3 "Architecture", api/ description) and
   `docs/LLD/query-agent.md` (pipeline order, module purposes) and `docs/LLD/rpc.md`
   (exposed/consumed RPCs -- confirms no query-pipeline RPC exists in `proto/hivemind.proto`).
5. Touched-file signatures only (not full bodies first): `def`/`class` grep over
   `agents/query/{intent_refiner,topic_selector,synthesizer}.py`, then targeted reads of each
   public entry point's exact signature and docstring.
6. `api/main.go`, `api/go.mod`, `engine/go.mod`, `go.work` -- confirmed `api/` has no router,
   no deps beyond stdlib, and `engine/` is the only module currently depending on
   `google.golang.org/grpc`.
7. `proto/hivemind.proto`'s `service HiveMind` block -- confirmed the RPC list (9 RPCs, none of
   them a query-pipeline entry point).

## Existing entrypoints (confirmed signatures)

- `agents/llm/client.py`: `class LLMClient(abc.ABC)` with abstract
  `complete(prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None) -> str`.
  `agents/llm/factory.py`: `create_llm_client(provider=None, **kwargs) -> LLMClient`.
- `agents/query/intent_refiner.py`:
  `refine_intent(query: str, history: Sequence[str], llm_client: LLMClient, *, model=None,
  temperature=0.0, max_tokens=None, timeout=None) -> IntentRefinerResult` where
  `IntentRefinerResult` has `.refined_intent: str`, `.entities: list[str]`,
  `.query_type: Literal["factual_lookup", "broad_exploratory"]`.
- `agents/query/topic_selector.py`:
  - `TopicCandidate(file_id: int, path: str, score: float)` (frozen dataclass).
  - `SearchCandidatesFn = Callable[[str, int], Sequence[TopicCandidate]]` (query, max_results).
  - `select_top_k(candidates: Sequence[TopicCandidate], *, k: int = DEFAULT_K) ->
    list[TopicCandidate]` (`DEFAULT_K = 3`).
  - `GraphNeighbor(file_id: int, edge_type: str, weight: int, hop: int)`.
  - `GraphNeighborsFn = Callable[[int, int], Sequence[GraphNeighbor]]` (file_id, hops).
  - `ExpansionResult(topic: TopicCandidate, neighbors: list[GraphNeighbor])`.
  - `expand_insufficient_topics(selected: Sequence[TopicCandidate], graph_neighbors:
    GraphNeighborsFn, *, hops: int = DEFAULT_EXPANSION_HOPS, ratio: float =
    DEFAULT_INSUFFICIENCY_RATIO) -> list[ExpansionResult]` (`DEFAULT_EXPANSION_HOPS = 2`,
    `DEFAULT_INSUFFICIENCY_RATIO = 0.5`).
  - `combine_and_cap(selected: Sequence[TopicCandidate], expansions: Sequence[ExpansionResult],
    *, k: int = DEFAULT_K) -> list[int]` -- returns deduplicated, order-preserved, `k + 2k`-capped
    `file_id` list. **Does not resolve paths/content.**
- `agents/query/synthesizer.py`:
  `synthesize_answer(refined_intent: str, query_type: str, entities: Sequence[str],
  selected_markdown: str, llm_client: LLMClient, *, model=None, temperature=0.0,
  max_tokens=None, timeout=None) -> SynthesizerResult` where `SynthesizerResult` has
  `.answer: str`, `.citations: list[str]`, `.provided_paths: list[str]`, and
  `.unknown_citations() -> list[str]`. `selected_markdown` must contain `"## File: <path>"`
  headers (regex `^##\s*File:\s*(?P<path>.+?)\s*$`, multiline) for path extraction to work.

## api/ gateway shape

- `api/` is its own Go module (`github.com/Aaryan123456679/HiveMind/api`), listed in the root
  `go.work` alongside `engine/`. No third-party router dependency is present or needed --
  stdlib `net/http.ServeMux` is sufficient and keeps `api/go.mod` dependency-free, matching
  the module's current (empty) state.
- No prior route file exists to mirror (`api/main.go` is `func main() {}`), so this subtask
  establishes the first route + the first `api/routes/` package.
- Per `docs/LLD/rpc.md`, the intended real transport from `api/` to the Python agent service
  for a query-pipeline call would be gRPC (mirroring the `engine/rpc` <-> `api/` pattern
  described in `docs/HLD.md`), but no such RPC is defined in `proto/hivemind.proto` yet -- this
  is the gRPC-boundary gap this subtask's `QueryPipeline` Go interface exists to abstract over,
  per the disclosed decision in `requirement.md`.

## Prior disclosed gaps consulted (from `.cdr/memory/pending.md`)
- SearchCandidates prefix-scan-only limitation (task 4.2.1, issue #21) -- not directly actionable
  by this subtask since `pipeline.py` takes `SearchCandidatesFn` injected, not a concrete client;
  noted but out of scope here.
- `topic_selector.py`'s own docstring-level disclosure of "no gRPC wiring yet" for
  `SearchCandidatesFn`/`GraphNeighborsFn` (issue #23) -- directly informs this subtask's DI
  decision (see requirement.md).
