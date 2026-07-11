# Requirement -- Subtask 5.2.4 (issue #27, milestone #7 / Phase 5)

**Title**: Shared final-answer LLM call wired identically across all three arms

**Source**: `gh issue view 27` (final subtask of this issue).

## Acceptance criteria (verbatim)

HiveMind, vector-RAG, and GraphRAG-lite all produce their final answer via the exact same
`agents/llm/` call path, so only the retrieval step varies between arms.

## Test spec (verbatim)

`pytest agents/eval/test_shared_final_llm.py`: assert all three arms invoke the same
`LLMClient` call signature/model config for final-answer generation.

## Impacted modules (verbatim)

- `agents/eval/pipeline.py` (new -- does not yet exist)
- `agents/eval/test_shared_final_llm.py` (new)

## Standing constraints (from task issuer, non-negotiable)

1. Ollama-only. No OpenRouter/Gemini, no `.env` file. Explicit `llm.ollama_client.OllamaClient`
   construction, mirroring 5.1.2/5.1.3/5.2.1/5.2.2/5.2.3's own precedent.
2. No new dependency; `agents/pyproject.toml` untouched.
3. Genuine identical-call-path *enforcement*, not just documentation/assertion: a single
   shared function must be the only way any arm generates its final answer, and the test must
   be able to fail if an arm secretly diverged (different prompt template/model/LLMClient
   construction).
4. Full 9-step CDR workflow; no self-verification (I4); one local commit, no push.
5. This is the last subtask of issue #27 -- issue should be closable after this lands
   (mirroring 5.1.3's closing of issue #26), per the launching agent's explicit instruction.

## What already exists (read directly, not secondhand)

- `agents/eval/baselines/vector_rag.py` (5.2.1): `retrieve_documents(query, index,
  embed_client, *, top_k, chunk_pool_size=None) -> list[str]`.
- `agents/eval/baselines/vector_rag_rerank.py` (5.2.2): `retrieve_documents_reranked(query,
  index, embed_client, *, top_k, rerank=False, llm_client=None, llm_model=None,
  candidate_pool_size=None, doc_texts=None) -> list[str]`.
- `agents/eval/baselines/graphrag_lite.py` (5.2.3): `retrieve_documents(query, graph,
  llm_client, *, top_k, max_hops=DEFAULT_MAX_HOPS, model=None) -> list[str]`.
- All three retrieval arms share the exact same output shape: ranked `list[str]` document ids.
- `agents/query/synthesizer.py` (issue #24, already implemented, production code): the real
  HiveMind final-answer generation call path. `synthesize_answer(refined_intent, query_type,
  entities, selected_markdown, llm_client, *, model=None, temperature=0.0, max_tokens=None,
  timeout=None) -> SynthesizerResult`. Builds a prompt embedding `selected_markdown` verbatim
  (expects `## File: <path>` headers), calls `llm_client.complete(prompt, model=model,
  temperature=temperature, max_tokens=max_tokens, timeout=timeout)`, parses the JSON
  `{answer, citations}` response.
- `docs/LLD/eval.md` (line 38) and `docs/LLD/llm-provider.md` (lines 39-41) *already document*
  the intended design: "All three arms share an identical final-answer LLM ... so that only the
  retrieval step varies between arms." This subtask makes that documented intent real/enforced
  in code, reusing `synthesizer.synthesize_answer` (the existing production call path) rather
  than inventing a second, parallel final-answer implementation.
- `agents/llm/client.py`: `LLMClient` ABC, single `complete()` method.
- `agents/llm/ollama_client.py`: `OllamaClient(LLMClient)`, `DEFAULT_MODEL = "llama3.1:8b"`.

## Design decision (recorded before implementation)

`agents/eval/pipeline.py` exposes exactly ONE function that performs final-answer generation --
`generate_final_answer(query, retrieved_doc_ids, corpus, llm_client, *, model=None)` -- which
internally calls `query.synthesizer.synthesize_answer` (reusing the real, already-implemented
production call path per the launching agent's explicit "reuse it if it exists" instruction).
Three thin per-arm wrapper functions (`run_hivemind_arm`, `run_vector_rag_arm`,
`run_graphrag_lite_arm`) each perform that arm's own retrieval, then call
`generate_final_answer` -- the *same* function object, not three near-identical copies. This is
the concrete enforcement mechanism: divergence is structurally impossible without editing
`pipeline.py` itself, and the test spies on `llm_client.complete()` to prove the same call
shape reaches the provider layer for all three arms.
