# Plan

## `agents/ingestion/shortlist.py`

- `TopicCandidate` frozen dataclass: `file_id: int`, `path: str`, `score: float`
  (final BM25-based relevance score, distinct from the engine's placeholder
  `CandidateTopic.score`).
- `SearchCandidatesFn = Callable[[str, int], Sequence[TopicCandidate]]` — the injection
  point tests mock; signature mirrors `SearchCandidatesRequest{query, max_results}` ->
  list of raw `TopicCandidate` (`score` field ignored/overwritten downstream).
- `GrpcSearchCandidatesClient` — minimal real gRPC client wrapper filling the
  disclosed gap: wraps `hivemind_pb2_grpc.HiveMindStub` over a caller-supplied
  `grpc.Channel`, `__call__(query, max_results) -> list[TopicCandidate]`, translating
  `hivemind_pb2.CandidateTopic` -> local `TopicCandidate`. Never imports `grpc`/the
  generated stubs at module import time in a way that breaks the mocked test path
  (import is at call time inside the class, so tests never need a running engine or a
  live channel).
- BM25 pure-Python helpers: `_tokenize(text) -> list[str]` (lowercase, split on
  non-alphanumeric, `/`, `_`, `-`, and camelCase boundaries — so topic paths like
  `billing/InvoiceDisputes` tokenize to `["billing", "invoice", "disputes"]`);
  `_bm25_scores(query_tokens, docs_tokens, k1=1.5, b=0.75) -> list[float]` (standard
  Okapi BM25 over the local candidate-pool "documents" = each topic's tokenized path).
- `shortlist(document_text, search_candidates, *, top_k=8, pool_size=200) ->
  list[TopicCandidate]`:
  1. Calls `search_candidates("", pool_size)` to get a bounded raw pool from the
     engine (empty-prefix query = "match everything", bounded by `pool_size` —
     this is the RPC's own division of labor: cheap, non-ranking bounded fetch).
  2. Locally BM25-ranks the pool's tokenized paths against the tokenized
     `document_text` (the "cheap local pre-filter" side — no network/model call).
  3. Returns the top `top_k` candidates by BM25 score, descending, each with `score`
     set to its BM25 score (ties broken by original pool order for determinism).
  4. If the pool is empty or `top_k >= len(pool)`, returns the (BM25-sorted) full pool
     — still bounded by construction since `pool_size` itself bounds the RPC call.

## `agents/ingestion/test_shortlist.py`

- Fixture: an "invoice dispute" document body plus a mocked pool of ~12 topic paths,
  a handful genuinely relevant (`billing/InvoiceDisputes`, `billing/PaymentDelays`)
  and the rest irrelevant (`hr/Onboarding`, `legal/NDATemplates`, ...).
  `search_candidates` mock asserts it's called with `("", pool_size)` and returns the
  fixture pool as `TopicCandidate` objects (real `SearchCandidates`/gRPC never
  touched).
- Test 1: shortlist size is bounded by `top_k` even though the mocked pool is much
  larger (parametrize `top_k` small vs. pool size).
  Test 2: shortlist size never exceeds pool size when pool < top_k.
- Test 3: relevant fixture topics appear, and rank ahead of (lower index than)
  irrelevant ones in the returned list.
- Test 4: `GrpcSearchCandidatesClient` is constructible without a live channel (uses a
  `unittest.mock` stand-in channel) and calls `HiveMindStub.SearchCandidates` with the
  expected request shape, translating the response — this covers the "gap-fill" client
  itself without a real network call.
