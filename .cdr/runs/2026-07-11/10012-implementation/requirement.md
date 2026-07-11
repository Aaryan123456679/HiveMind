# Requirement — Subtask 5.2.2

**Issue:** #27 (milestone #7, Phase 5) — "Reranking stage for vector-RAG baseline (time-permitting)"

**Acceptance criteria (verbatim from dispatch):**
> A reranking step (e.g. cross-encoder) can be toggled on the vector-RAG baseline and measurably
> improves precision@k on a fixture set when enabled.

**Test spec:** `pytest agents/eval/test_vector_rag_rerank.py`: compare precision@k reranking on
vs. off on a fixture set.

**Impacted modules (declared scope):**
- `agents/eval/baselines/vector_rag_rerank.py` (new)
- `agents/eval/test_vector_rag_rerank.py` (new)

**Standing constraints (from `.cdr/memory/pending.md`, both re-confirmed applicable here):**
1. Ollama-only for any LLM-backed work in this repo — no OpenRouter/Gemini, no `.env`.
2. Subtasks 5.2.1–5.2.4 are explicitly user-authorized as fixture/local-only work (not live
   large-corpus benchmark execution) — this subtask (5.2.2) is inside that authorized set, so no
   extra go-ahead is required before proceeding.

**Relationship to 5.2.1 (already merged, commit `573478a3972493c0f4f53ae81cbae4b968d6cac2`):**
`docs/HLD.md`/`docs/LLD/eval.md` describe reranking as an optional *addition* to the same
vector-RAG baseline arm ("real chunk size/overlap, reranking if time allows"), not a separate
retrieval path. This subtask must build on top of 5.2.1's `agents/eval/baselines/vector_rag.py`
(`chunk_document`, `OllamaEmbeddingClient`, `VectorRagIndex`, `retrieve_documents`) rather than
reimplement chunking/embedding/retrieval.

**Key open design question (must be resolved in architecture-discovery.md before implementing):**
Whether "reranking" here means a real cross-encoder model (would require a new heavyweight ML
dependency — torch/sentence-transformers — not currently in `agents/pyproject.toml`) or an
Ollama-LLM-based pairwise/listwise reranker (no new dependency, consistent with the repo's
existing Ollama-only LLM client and 5.2.1's own "reuse existing httpx, no new pip package"
precedent).
