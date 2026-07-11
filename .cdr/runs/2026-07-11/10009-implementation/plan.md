# Plan — Subtask 5.2.1

## Files to create

### 1. `agents/eval/baselines/__init__.py`
Empty package marker (matches `agents/eval/__init__.py`'s own empty-module convention).

### 2. `agents/eval/baselines/vector_rag.py`
- Module docstring: fairness rationale (condensed from architecture-discovery.md), disclosed
  design choices, explicit fixture-only self-test scope boundary.
- `OllamaEmbeddingClient`: small local class wrapping `POST {base_url}/api/embed` (Ollama's
  batch-capable embedding endpoint). Constructor: `base_url`, `model="nomic-embed-text"`,
  `timeout`, `transport` (mirrors `OllamaClient`'s test-injection pattern). Method
  `embed(texts: list[str]) -> list[list[float]]`. Raises a dedicated
  `OllamaEmbeddingError` (subclass of `Exception`, not `llm.client.LLMError` -- this is not an
  `LLMClient`, deliberately, since it embeds rather than completes) on any HTTP/parse failure.
- `Chunk` frozen dataclass: `doc_id`, `chunk_id`, `text`, `start_word`, `end_word`.
- `chunk_document(doc_id, text, *, chunk_size_words, overlap_words) -> list[Chunk]`: fixed-size
  sliding window over whitespace-split words, snapping chunk text back to a `" ".join(...)` of
  whole words only (never mid-word). Validates `0 <= overlap_words < chunk_size_words`.
- `DEFAULT_CHUNK_SIZE_WORDS` / `DEFAULT_OVERLAP_WORDS`: tuned constants (values finalized after
  the self-consistency grid-search sweep; placeholders during initial write, updated in the same
  commit once the sweep runs).
- `CHUNK_SIZE_CANDIDATES = (80, 150, 220)`, `OVERLAP_FRACTION_CANDIDATES = (0.0, 0.15, 0.30)`:
  the tuning grid.
- `select_chunk_config(fixture_docs, fixture_queries, embed_fn, *, top_k=3) -> tuple[int, int]`:
  runs the grid, returns `(chunk_size_words, overlap_words)` maximizing mean recall@top_k across
  `fixture_queries`. Used by the tuning pass (recorded in self-consistency.json), not called at
  baseline-runtime (avoids re-tuning on every retrieval call).
- `VectorRagIndex`: `build(chunks, embed_client) -> VectorRagIndex` classmethod (embeds all
  chunk texts once, batched); `search(query, embed_client, top_k) -> list[tuple[Chunk, float]]`
  (cosine similarity ranking, pure Python).
- `retrieve_documents(query, index, embed_client, top_k) -> list[str]`: chunk-level search,
  max-score-per-doc_id aggregation, returns ranked `doc_id`s.
- `recall_at_k(retrieved_doc_ids, relevant_doc_ids, k) -> float`: simple recall metric helper
  used by both the tuning sweep and the shipped test.

### 3. `agents/eval/test_vector_rag_baseline.py`
- Fixture corpus: ~6 short synthetic documents (plain strings + doc_ids), each about a distinct
  topic, mirroring the "policy/manual" flavor of the real synthetic corpus but self-contained
  (no dependency on `data/synthetic_corpus/`, per the explicit fixture-only scope).
- Fixture queries: ~4 queries, each with a known set of relevant `doc_id`s.
- Pure-unit tests (no network, always run): `chunk_document` boundary behavior (word-snapping,
  overlap correctness, `ValueError` on invalid overlap), `recall_at_k` arithmetic.
- Live-local-embedding test(s) (mirrors `test_e2e_smoke.py`'s skip-if-unreachable convention via
  a module-level `_ollama_embeddings_available()` check against `/api/tags` for
  `nomic-embed-text`, `pytestmark = pytest.mark.skipif(...)`):
  - Build a `VectorRagIndex` from the fixture corpus chunks using the real
    `OllamaEmbeddingClient`.
  - For each fixture query, call `retrieve_documents` and assert recall@3 across the fixture
    query set meets a reasonable bar (>= 0.75 mean, matching the test spec's "assert reasonable
    recall on known-relevant chunks" wording).
  - A second live test asserting `select_chunk_config` runs without error, returns a config
    from the declared grid, and its winning config matches (or is consistent with) the module's
    shipped `DEFAULT_CHUNK_SIZE_WORDS`/`DEFAULT_OVERLAP_WORDS` constants -- this is the
    "actual tuning" proof point, not just a docstring claim.

## Implementation order
1. `__init__.py`.
2. `vector_rag.py` core (chunker + recall_at_k + embedding client + index/search) with
   placeholder default constants.
3. `test_vector_rag_baseline.py` unit tests (non-network) -- run, confirm green.
4. Run the real tuning sweep (`select_chunk_config` against the fixture corpus/queries) via a
   throwaway local script/REPL call against the real Ollama server; record the winning config
   and full per-candidate recall numbers in self-consistency.json.
5. Update `DEFAULT_CHUNK_SIZE_WORDS`/`DEFAULT_OVERLAP_WORDS` in `vector_rag.py` to the winning
   config from step 4.
6. Run the full test file (including live tests) against the real local Ollama server; confirm
   all pass with real (non-mocked) embeddings.
7. Ruff clean.

## Rollback plan
All new files; rollback is `git rm` of the three new paths (no existing file touched).
