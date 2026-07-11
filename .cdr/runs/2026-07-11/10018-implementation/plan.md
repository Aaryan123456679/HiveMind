# Plan -- Subtask 5.2.4

## `agents/eval/pipeline.py` (new)

1. Module docstring disclosing: purpose, the "reuse `query.synthesizer.synthesize_answer`,
   don't invent a parallel implementation" decision, the enforcement mechanism (single shared
   function all three arms literally call), and the Ollama-only/no-new-dependency constraints.
2. `_build_selected_markdown(retrieved_doc_ids: Sequence[str], corpus: Mapping[str, str]) ->
   str`: renders `## File: <doc_id>\n\n<text>\n\n` per id, in ranked order, skipping any id
   missing from `corpus` (disclosed, not raised -- an eval baseline's retrieved id list is not
   guaranteed to be a corpus superset in a fixture test, and silently including only what's
   resolvable mirrors `synthesizer.py`'s own file-path-header-driven-by-what's-present
   convention).
3. `generate_final_answer(query, retrieved_doc_ids, corpus, llm_client, *, model=None,
   temperature=0.0, max_tokens=None, timeout=None) -> SynthesizerResult`: the ONE shared
   final-answer function. Builds `selected_markdown` via step 2, then calls
   `query.synthesizer.synthesize_answer(refined_intent=query, query_type="eval_benchmark",
   entities=(), selected_markdown=selected_markdown, llm_client=llm_client, model=model,
   temperature=temperature, max_tokens=max_tokens, timeout=timeout)` and returns its result
   unmodified. `query_type="eval_benchmark"` and `entities=()` are fixed literal constants (not
   parameters) -- deliberately so that no caller can vary them per-arm, which would reintroduce
   exactly the per-arm prompt divergence this subtask exists to prevent.
4. Three arm-runner wrapper functions, each: (a) perform that arm's own already-implemented
   retrieval, (b) call `generate_final_answer` -- the same function from step 3 -- with the
   query, that arm's retrieved doc ids, and the same corpus/llm_client/model:
   - `run_hivemind_arm(query, retrieved_doc_ids, corpus, llm_client, *, model=None) ->
     SynthesizerResult`. HiveMind's own real retrieval already lives in `agents/query/pipeline
     .py`'s `run_query_pipeline()` (gRPC-backed, out of this subtask's scope to re-invoke here);
     this wrapper accepts an already-retrieved doc-id list as an explicit parameter (documented
     as such) so the benchmark harness can drive the HiveMind arm's retrieved-id list from
     whatever future subtask wires the real end-to-end call, while still forcing its
     final-answer step through the identical shared function today.
   - `run_vector_rag_arm(query, index, embed_client, corpus, llm_client, *, top_k, model=None)
     -> SynthesizerResult`: calls `baselines.vector_rag.retrieve_documents(query, index,
     embed_client, top_k=top_k)`, then `generate_final_answer`.
   - `run_graphrag_lite_arm(query, graph, corpus, llm_client, *, top_k,
     max_hops=DEFAULT_MAX_HOPS, model=None) -> SynthesizerResult`: calls
     `baselines.graphrag_lite.retrieve_documents(query, graph, llm_client, top_k=top_k,
     max_hops=max_hops)`, then `generate_final_answer`.

## `agents/eval/test_shared_final_llm.py` (new)

- `_SpyLLMClient(LLMClient)`: records every `complete()` call's full kwargs into
  `self.calls: list[dict]`, and returns a canned response selected by lightweight
  prompt-content sniffing (mirrors `test_graphrag_baseline.py`'s `_StubLLMClient` convention):
  entity-extraction prompts get canned entity-list JSON, rerank prompts get canned
  reorder-index JSON, synthesizer prompts (recognizable by the `## File:` header / "citations"
  instruction text) get canned `{"answer": ..., "citations": [...]}"` JSON.
- Build a tiny fixture corpus (3-4 short docs) + a `VectorRagIndex`-shaped fixture (using a
  `_StubEmbeddingClient` returning fixed vectors, since embeddings are out of `LLMClient`'s
  scope) + an `EntityGraph` built via the spy client.
- Run all three wrappers with the SAME `query`, SAME `llm_client` instance, SAME `model="
  llama3.1:8b"` override, and (for HiveMind arm) a directly-supplied `retrieved_doc_ids` list.
- Core assertion (the one that would fail if an arm silently diverged): take, for each arm, the
  *last* recorded call in `spy.calls` immediately after that arm runs (the final-answer call --
  provably the last one, since `generate_final_answer` always runs last in every wrapper) and
  assert its `model`, `temperature`, `max_tokens`, `timeout` are identical across all three
  arms, and that its prompt contains the fixed literal markers this subtask's shared
  `_build_prompt`/`synthesize_answer` always emits (the "query_type" and instruction text),
  proving it went through the *same* prompt template, not merely the same kwargs by
  coincidence.
- A second, more direct test: monkeypatch `eval.pipeline.synthesize_answer` itself with a
  `unittest.mock.Mock` wrapping the real function, run all three arms, and assert
  `mock.call_count == 3` with each call's `query_type`/`entities` args identical literal
  values -- this is the test that would fail immediately if a future edit made e.g. the
  GraphRAG-lite wrapper call some other function instead of `generate_final_answer`.
- A negative/regression-guard test: directly assert `run_vector_rag_arm`,
  `run_graphrag_lite_arm`, and `run_hivemind_arm`'s bytecode/source all route through the same
  `generate_final_answer` function object (`inspect.getsource` contains the exact call, or
  simpler: patch `eval.pipeline.generate_final_answer` and assert it is called exactly once by
  each wrapper) -- structural proof, not just observational.

## No changes needed elsewhere

`agents/query/synthesizer.py`, `agents/llm/*`, and the three baseline modules are read-only
dependencies; `agents/pyproject.toml` untouched; no `.env` file created or read; all `LLMClient`
usage is via directly-constructed `OllamaClient` in the test's live-tier (if added) or a spy
stub (unit tier) -- Ollama-only.
