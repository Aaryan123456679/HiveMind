# Plan — Subtask 5.2.2

## New module: `agents/eval/baselines/vector_rag_rerank.py`

1. `precision_at_k(retrieved_doc_ids, relevant_doc_ids, k) -> float` — fraction of top-k retrieved
   that are relevant (`|top_k ∩ relevant| / k`; `1.0` if `k == 0`, matching `recall_at_k`'s
   vacuous-case convention style but for the precision denominator).
2. `@dataclass(frozen=True) RerankCandidate(doc_id: str, text: str)` — one document's full text for
   reranking purposes (fixture-scale: whole document text, not just best chunk).
3. `build_rerank_prompt(query, candidates: list[RerankCandidate]) -> str` — a single listwise
   prompt: numbered candidates with truncated text, instructing the model to output an ordered,
   comma-separated list of candidate numbers, best-match first, and nothing else.
4. `parse_rerank_order(response_text, num_candidates) -> list[int]` — extract integers from the
   LLM's free-form response in order of first appearance, keep only those in `[1, num_candidates]`,
   dedupe preserving first occurrence, then append any missing indices (in original order) at the
   end — guarantees a full permutation of `1..num_candidates` is always returned even if the model's
   output is malformed/partial/verbose.
5. `rerank_documents(query, candidates, llm_client: LLMClient, *, model=None) -> list[str]` — builds
   the prompt, calls `llm_client.complete(...)`, parses the order, returns candidate `doc_id`s in
   that order.
6. `retrieve_documents_reranked(query, index, embed_client, *, top_k, rerank, llm_client=None,
   candidate_pool_size=None, doc_texts) -> list[str]`:
   - `candidate_pool_size` defaults to `top_k` when `rerank=False` (matches plain
     `retrieve_documents(top_k=top_k)` exactly) and to a larger pool (default `top_k + 2`, override-
     able) when `rerank=True`.
   - Calls 5.2.1's `retrieve_documents(query, index, embed_client, top_k=candidate_pool_size)`
     unmodified.
   - If `rerank`: build `RerankCandidate`s from `doc_texts` for each returned doc id (raises
     `KeyError`-style `ValueError` if a doc id has no text -- fail loud, no silent skip), call
     `rerank_documents`, truncate to `top_k`.
   - Else: truncate the vector-ranked list directly to `top_k`.
   - Requires `llm_client` when `rerank=True` (raises `ValueError` if omitted).

Module docstring discloses: relationship to 5.2.1 (reuse, not reimplementation), the
cross-encoder-vs-Ollama-LLM tradeoff and decision (mirroring architecture-discovery.md section 3),
the precision@k/candidate-pool design rationale (section 2), and the local-only/no-new-dependency
guarantee.

## New test file: `agents/eval/test_vector_rag_rerank.py`

- Pure-unit tests (no network): `precision_at_k` edge cases; `parse_rerank_order` on well-formed,
  malformed, partial, and out-of-range LLM outputs; `retrieve_documents_reranked(rerank=False)`
  equivalence to `retrieve_documents` directly (mocked/no-network path using `vector_rag`'s own
  `httpx.MockTransport` injection point is not strictly needed here since this path never calls the
  LLM, but embedding calls still need a real/skippable Ollama -- so this equivalence test lives in
  the live-local tier below, reusing the same fixture).
- Live-local tier (skip-if-Ollama-or-model-unreachable, mirroring 5.2.1's and
  `agents/ingestion/test_e2e_smoke.py`'s convention; checks both `nomic-embed-text` and
  `llama3.1:8b` are pulled):
  - A dedicated fixture corpus + one query engineered (per architecture-discovery.md section 4) so
    plain vector top-k precision is provably imperfect (a lexically-similar-but-off-topic distractor
    outranks the true relevant doc within the candidate pool but the true doc is recoverable within
    a slightly larger pool).
  - Test asserts: `rerank=False` precision@k < 1.0 on this fixture (documents the discriminating
    case is real, not fabricated) AND `rerank=True` precision@k on the same fixture is strictly
    greater than the `rerank=False` value (the actual "reranking measurably improves precision@k"
    acceptance criterion).
  - Equivalence test: `retrieve_documents_reranked(..., rerank=False)` returns exactly
    `retrieve_documents(..., top_k=top_k)` on the same fixture/index.

## Execution order
1. Implement `vector_rag_rerank.py`.
2. Implement pure-unit tests; run them (no network needed).
3. Implement live-local tests against real local Ollama; iterate the fixture corpus/query
   empirically (per architecture-discovery.md section 4) until the discriminating case is real
   (rerank-off precision@k < rerank-on precision@k, both from real runs, not asserted).
4. Record the actual numbers in self-consistency.json.
5. `ruff check` + full `agents/eval/` test run.
6. One local commit (Problem/Solution/Impact format, matching `.cdr/commits/task-5.2.1.md` style).
7. handoff.json.
