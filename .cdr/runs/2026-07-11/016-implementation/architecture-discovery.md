# Architecture Discovery — 4.4.1 topic_selector.py

## Order followed
index/ (none exist for agents/query beyond LLD) -> memory/handoffs (none found
for this subtask) -> docs/LLD/query-agent.md (targeted LLD) -> touched-file
neighborhood (`agents/query/*.py`) -> source precedent
(`agents/ingestion/shortlist.py`, `agents/ingestion/wiring.py`,
`agents/query/intent_refiner.py`) -> proto (`proto/hivemind.proto`).

## LLD contract (`docs/LLD/query-agent.md`)
- `topic_selector.py` receives a candidate topic list from the Go-side
  non-LLM `SearchCandidates` RPC.
- Selects top-`k` topics, `k` tunable, default 3.
- (Future, not this dispatch) may request 0-2 hop `GraphNeighbors` expansion
  for topics judged "insufficient alone".
- (Future, not this dispatch) hard cap of `k + 2k` total files on the
  combined result — system-wide invariant per HLD §7.

## Proto shapes (`proto/hivemind.proto`)
- `SearchCandidatesRequest{query, max_results}` / `SearchCandidatesResponse{repeated CandidateTopic candidates}`.
- `CandidateTopic{file_id: uint64, path: string, score: float}`.
- `GraphNeighborsRequest{file_id, depth (0-2), edge_type_filter, max_nodes}` /
  `GraphNeighborsResponse{repeated Neighbor neighbors}` — relevant only to
  4.4.2, not built now, but shapes future extension point.

## Precedent: `agents/ingestion/shortlist.py` (task 3.4.2)
Directly analogous "already fetched ranked/scored candidates from
SearchCandidates, now do a local bounded-selection step" pattern in the same
repo:
- Defines its own plain frozen dataclass `TopicCandidate(file_id, path, score)`
  decoupled from the gRPC-generated `CandidateTopic` message.
- Defines a `SearchCandidatesFn = Callable[[str, int], Sequence[TopicCandidate]]`
  Protocol-style type alias as the injection point, so tests supply a plain
  mock callable — no real/mocked gRPC channel needed to unit-test the local
  selection logic.
- Provides a `GrpcSearchCandidatesClient` class as a *separate*, real
  gRPC-backed implementation of `SearchCandidatesFn`, with lazy import of
  `hivemind_pb2`/`hivemind_pb2_grpc` inside `__init__`/`__call__` (never at
  module import time) plus a `sys.path` fallback so importing/testing the
  core selection function never requires `grpc` to be importable.
- The core function itself (`shortlist(...)`) takes an already-fetched or
  injected-callable pool, not a raw gRPC response object, and returns plain
  `TopicCandidate` values — fully decoupled, deterministic, testable with a
  fixture list.

## Precedent: `agents/query/intent_refiner.py` (task 4.3.1, same package)
- Establishes the module-docstring convention used across `agents/`: long
  "disclosed choice" sections explaining any LLD ambiguity resolved by the
  implementer, citing exact LLD wording.
- Establishes `TYPE_CHECKING`-only imports for typing-only symbols, `from
  __future__ import annotations`, dataclass-based structured results.
- This package (`agents/query/`) has no gRPC-wiring module yet (per the
  dispatch note, that's likely issue #25's job) — so `topic_selector.py`
  should NOT itself decide how the real RPC channel gets built; it should
  only depend on an injected callable, exactly like `shortlist.py`'s
  `SearchCandidatesFn` pattern, deferring the "real gRPC client" question
  to whichever subtask actually wires `agents/query/`'s gRPC clients (out of
  scope here — no `wiring.py`/gRPC-client class needed in `agents/query/`
  yet, since nothing in 4.4.1's acceptance criteria or test spec asks for
  one, unlike 3.4.2 which explicitly disclosed and filled that gap for
  `agents/ingestion/`).

## Design decision for 4.4.1 (extensible-but-not-overbuilt shape)
- `TopicCandidate` — frozen dataclass `(file_id: int, path: str, score: float)`,
  mirroring `ingestion.shortlist.TopicCandidate` structurally (same field
  names/types), independent module-local definition (no cross-package import
  from `ingestion`, keeping `agents/query/` decoupled from
  `agents/ingestion/`'s internals — the two packages are siblings under
  `agents/`, not layered).
- `SearchCandidatesFn` type alias — `Callable[[str, int], Sequence[TopicCandidate]]`,
  same shape as `ingestion.shortlist.SearchCandidatesFn`, for future callers
  that want to fetch the pool themselves via injected RPC callable. **Not**
  required by 4.4.1's own function signature (see below) — 4.4.1's test spec
  says "against a fixture candidate list", i.e. the function should accept
  an already-obtained candidate list directly, not fetch it itself. Keeping
  the type alias defined but unused-by-the-public-function-signature would
  be premature; instead we only add it once 4.4.2 needs an injected-fetch
  entry point for `GraphNeighbors`. For 4.4.1, the public function takes
  `candidates: Sequence[TopicCandidate]` directly — this is what "Given a
  SearchCandidates result" means concretely: the caller has already
  obtained/decoded the `SearchCandidatesResponse` into `TopicCandidate`s
  (e.g. via future issue #25 gRPC wiring, or a test fixture) and hands the
  list to the selector.
- Public function: `select_top_k(candidates: Sequence[TopicCandidate], *,
  k: int = DEFAULT_K) -> list[TopicCandidate]`. Free function (not a class)
  because 4.4.1 has no internal state to carry across calls yet. This
  mirrors `shortlist()`'s own free-function shape exactly.
- Extensibility for 4.4.2/4.4.3 (design-only, not built): the LLD's pipeline
  is "selector picks top-k, *then* may request expansion per-topic, *then*
  hard-caps the combined result". A single free function with a `k` param
  is naturally composable — 4.4.2 will likely add a second function (e.g.
  `select_topics_with_expansion(...)`) that calls `select_top_k(...)`
  internally then loops over the results deciding expansion, and 4.4.3 will
  wrap/cap the combined output. No class or shared-state object is needed
  for that composition, so introducing one now would be premature
  over-engineering not asked for by 4.4.1's acceptance criteria. We do,
  however, name the constant `DEFAULT_K = 3` (not a magic literal) and put
  it at module level, consistent with `shortlist.py`'s `DEFAULT_TOP_K`/
  `DEFAULT_POOL_SIZE` precedent, so 4.4.2/4.4.3 can import/reuse it.

## Files touched
- New: `agents/query/topic_selector.py`
- New: `agents/query/test_topic_selector.py`
- No changes to `docs/LLD/query-agent.md` (already fully specifies 4.4.1;
  no LLD drift), no proto/engine changes (already merged per #21/4.2.1),
  no changes to `agents/ingestion/*`.
