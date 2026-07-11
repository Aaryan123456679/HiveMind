# Architecture discovery -- Subtask 5.2.4

Read order followed: `.cdr/index/*` -> prior 5.2.1/5.2.2/5.2.3 handoffs -> `docs/HLD.md` (via
`docs/LLD/eval.md`/`llm-provider.md` cross-refs) -> `docs/LLD/eval.md`, `docs/LLD/llm-provider.md`,
`docs/LLD/query-agent.md` -> touched-file-adjacent source (`agents/query/synthesizer.py`,
`agents/llm/client.py`, `agents/llm/ollama_client.py`, the three baseline modules) -> test
conventions (`agents/query/conftest.py`'s `FakeLLMClient`, `agents/eval/test_graphrag_baseline.py`'s
`_StubLLMClient`).

## `.cdr/index/task.jsonl` findings

- `task-5.2.1` / `5.2.2` / `5.2.3`: all `state: verified`, all issue 27. Each left a `notes` field
  disclosing non-blocking findings (fixture-scale tuning caveats) -- none block this subtask;
  none touch `agents/eval/pipeline.py` or the final-answer step.

## `docs/LLD/eval.md` (already read in full)

Declares 3 retrieval arms (HiveMind / classic vector-RAG / GraphRAG-lite) and explicitly states
(line 38): "All three arms share an identical final-answer LLM (via `llm-provider.md`) so that
only the retrieval step varies between arms." This subtask is the concrete implementation of
that already-documented intent -- no new design decision needed there, just enforcement.

## `docs/LLD/llm-provider.md` (already read in full)

Confirms (lines 39-41) the eval harness's three arms share "an identical final-answer LLM call"
that "also goes through this interface" (`agents/llm/`'s `LLMClient`). No code changes needed in
`agents/llm/` itself for this subtask -- it is a pure consumer.

## `docs/LLD/query-agent.md` (already read in full)

Documents the *actual, already-implemented* HiveMind final-answer call path:
`intent_refiner -> topic_selector -> synthesizer -> answer`, with `synthesizer.py`'s
`synthesize_answer()` performing "Final LLM call: refined intent + concatenated selected
markdown (with file-path headers) -> answer with inline file-path citations." This is the
call path this subtask must make vector-RAG and GraphRAG-lite also go through -- not a new,
parallel implementation.

## Source read directly (not secondhand)

- `agents/query/synthesizer.py`: full file read. Confirmed `synthesize_answer(refined_intent,
  query_type, entities, selected_markdown, llm_client, *, model=None, temperature=0.0,
  max_tokens=None, timeout=None) -> SynthesizerResult`. Builds prompt via `_build_prompt`
  (expects `## File: <path>` headers in `selected_markdown`, extracted via `_FILE_HEADER_RE =
  re.compile(r"^##\s*File:\s*(?P<path>.+?)\s*$", re.MULTILINE)`), calls exactly one
  `llm_client.complete(prompt, model=model, temperature=temperature, max_tokens=max_tokens,
  timeout=timeout)`, parses JSON `{"answer": str, "citations": [str]}`.
- `agents/llm/client.py`, `agents/llm/ollama_client.py`: full files read. `LLMClient.complete()`
  signature confirmed identical to what `synthesizer.py` calls. `OllamaClient` is the only
  concrete provider this project's eval subtasks are permitted to construct (per standing
  constraint); `DEFAULT_MODEL = "llama3.1:8b"`.
- `agents/eval/baselines/vector_rag.py`: `retrieve_documents(query, index, embed_client, *,
  top_k, chunk_pool_size=None) -> list[str]`. `VectorRagIndex.build(chunks, embed_client)`.
  Never calls `LLMClient` itself (uses a separate `OllamaEmbeddingClient`, disclosed by 5.2.1 as
  deliberately out of `LLMClient`'s scope).
- `agents/eval/baselines/vector_rag_rerank.py`: `retrieve_documents_reranked(query, index,
  embed_client, *, top_k, rerank=False, llm_client=None, llm_model=None,
  candidate_pool_size=None, doc_texts=None) -> list[str]`. When `rerank=True` it *does* call
  `llm_client.complete()` once for reranking -- a real, but distinct, LLM call from
  final-answer generation. This subtask's test must not confuse a rerank call with the
  final-answer call when spying on `llm_client.complete()`.
- `agents/eval/baselines/graphrag_lite.py`: `retrieve_documents(query, graph, llm_client, *,
  top_k, max_hops=DEFAULT_MAX_HOPS, model=None) -> list[str]`. `EntityGraph.build(docs,
  llm_client, *, model=None)`. Also calls `llm_client.complete()` (via `extract_entities`) for
  entity extraction -- again, a real but distinct call from final-answer generation.
- `agents/query/conftest.py`: `FakeLLMClient(LLMClient)` -- canned-response stub that records
  every call's `(prompt, model, temperature, max_tokens, timeout)` into `self.calls`. This
  subtask's own stub (in the new test file, per 5.2.1-5.2.3's precedent of not reaching into
  another package's test conftest) mirrors this shape, extended to serve different canned JSON
  depending on which call it is (entity-extraction JSON vs. rerank-order JSON vs.
  synthesizer's `{"answer":..., "citations":[...]}` JSON), keyed on lightweight prompt-content
  sniffing (same convention `test_graphrag_baseline.py`'s `_StubLLMClient` already uses).
- Editable install confirmed (`agents/.venv/.../__editable__.hivemind_agents-0.1.0.pth`):
  `agents/` itself is the import root, so `from eval.pipeline import ...`, `from
  query.synthesizer import synthesize_answer`, `from llm.ollama_client import OllamaClient` are
  the correct import forms (matching every existing file in `agents/eval/` and `agents/query/`).

## Conclusion

No existing "shared harness" module exists yet; `agents/eval/pipeline.py` is a pure addition.
The one already-implemented, production final-answer call path (`query.synthesizer
.synthesize_answer`) is reused as-is (constraint: "reuse it if it exists rather than inventing
a parallel one") -- zero changes needed to `agents/query/synthesizer.py`, `agents/llm/*`, or any
of the three baseline modules.
