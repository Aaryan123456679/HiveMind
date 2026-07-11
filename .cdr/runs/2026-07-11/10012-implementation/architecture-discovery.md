# Architecture Discovery — Subtask 5.2.2 (reranking stage for vector-RAG baseline)

## 1. What "reranking" needs to mean here, precisely

`docs/HLD.md` #7 and `docs/LLD/eval.md` both describe reranking as an *optional addition to the
same vector-RAG arm* ("real chunk size/overlap, reranking **if time allows**"), not a separate
retrieval path — reinforced by the dispatch's own impacted-module list
(`agents/eval/baselines/vector_rag_rerank.py`, not a new `baselines/` sibling arm). Standard
retrieve-then-rerank architecture: a cheap first-stage retriever (5.2.1's cosine-similarity vector
search) pulls a larger *candidate pool*, then a more expensive/more accurate second-stage reranker
re-scores that pool and the result is truncated to the final `top_k`.

This module therefore **reuses 5.2.1's `vector_rag.py` verbatim** (`chunk_document`,
`OllamaEmbeddingClient`, `VectorRagIndex`, `retrieve_documents`) for first-stage retrieval and adds
only a second-stage reranker on top, toggleable on/off.

## 2. Precision@k vs. recall@k — why the metric choice matters for *this* subtask

5.2.1 used recall@k (fraction of relevant docs present in top-k). This subtask's acceptance
criteria and test spec explicitly ask for **precision@k** (fraction of top-k that are relevant),
which `vector_rag.py` does not currently define. Added `precision_at_k` to the new module (not to
`vector_rag.py`, keeping 5.2.1's file untouched per the declared impacted-module scope).

Critically: **reordering the members already inside a fixed top-k list changes neither precision@k
nor recall@k** (both are set-membership metrics over the top-k slice, order-insensitive). So for
reranking to "measurably improve precision@k," the reranker must have the power to change *which*
documents end up inside the final top-k — i.e. rerank over a **candidate pool strictly larger than
top_k** pulled from vector search, then truncate to `top_k` only *after* reranking. This shapes the
implementation: `retrieve_documents_reranked(..., candidate_pool_size, rerank)` retrieves
`candidate_pool_size` candidates via 5.2.1's `retrieve_documents`, and only if `rerank=True` does a
second-stage LLM rerank before truncating to `top_k`; if `rerank=False` it truncates the original
vector-ranked list directly to `top_k` (mathematically identical to calling
`retrieve_documents(top_k=top_k)` directly — confirmed by a dedicated equivalence test).

## 3. Cross-encoder model vs. Ollama-LLM-based reranking — evaluated per dispatch instruction

Checked `agents/pyproject.toml` first, as instructed: dependencies are
`fastapi, uvicorn, grpcio, grpcio-tools, protobuf, pydantic, httpx, pymupdf` (+ dev
`pytest, ruff`). **No `torch`, `sentence-transformers`, or any ML/tensor library is present.**

**Option A — real cross-encoder model** (e.g. `cross-encoder/ms-marco-MiniLM-L-6-v2` via
`sentence-transformers`):
- Pro: purpose-built for exactly this task; well-established, fast at fixture scale once loaded.
- Con: requires adding `sentence-transformers` (which pulls in `torch` transitively) — a
  heavyweight new dependency (hundreds of MB, GPU/CPU wheel selection complexity) purely for one
  optional ("if time allows") reranking step, plus a new model download outside Ollama's existing
  model-pull flow that 5.2.1 and `agents/llm/ollama_client.py` already standardize on. This cuts
  directly against 5.2.1's own explicitly disclosed precedent ("reused existing `httpx` rather
  than adding a new embedding library") and the repo's Ollama-only-for-LLM-work standing
  preference.

**Option B — Ollama-LLM-based pairwise/listwise reranking** (prompt the already-running local
`llama3.1:8b` via the existing `agents/llm/client.py` `LLMClient`/`OllamaClient` abstraction to
score or reorder a candidate list):
- Pro: **zero new dependencies** (reuses `httpx` transitively via `OllamaClient`, and reuses the
  existing `LLMClient.complete(prompt) -> str` interface *exactly as designed* — unlike 5.2.1,
  which had to define its own `OllamaEmbeddingClient` because embeddings are a different call
  shape not covered by `LLMClient`, reranking via prompted text-in/text-out completion is *exactly*
  `LLMClient`'s contract, so this module can depend on `agents/llm/client.py` directly with no new
  abstraction). Fully local, no paid API, no `.env`. Consistent with the project's Ollama-only
  standing constraint and its "prefer reuse over new dependency" pattern.
- Con: less standard/less proven than a dedicated cross-encoder; response parsing (turning free-form
  LLM text into a ranking) is more fragile than a model that natively outputs a scalar score;
  slower per call than a small cross-encoder forward pass (mitigated at fixture scale — one call
  per query, a handful of short candidate texts).

**Decision: Option B (Ollama-LLM-based listwise reranking).** Given (1) this subtask is explicitly
"time-permitting" scope, not core-path infrastructure, (2) the acceptance criterion only requires
"a reranking step (e.g. cross-encoder)" — the "e.g." disclosing that a cross-encoder is one
example, not a mandate — and (3) a lightweight local approach can plausibly satisfy "measurably
improves precision@k on a fixture set" (confirmed empirically in self-consistency.json, not just
asserted here), adding `sentence-transformers`/`torch` is not justified. This choice is disclosed
in the module's own docstring for the same transparency reason 5.2.1 disclosed its embedding-model
and dependency choices.

## 4. Fixture design — why a naive reuse of 5.2.1's fixture would not discriminate

5.2.1's own fixture corpus/queries were tuned so that recall@3 is already at or near 1.0 with plain
vector search (see 5.2.1's `select_chunk_config` sweep) — deliberately, since 5.2.1's job was to
prove the *baseline* is well-tuned, not to prove reranking helps. Reusing that fixture verbatim for
*this* subtask would risk reranking having literally nothing to fix (candidate pool already
perfectly ordered), which would either produce a no-op "improvement" (0.0 -> 0.0 delta, not
"measurable") or force fabricating a discriminating case dishonestly.

Per dispatch instruction, this subtask ships **its own dedicated fixture corpus/queries**,
deliberately constructed to contain a genuine discriminating case: a candidate pool where the true
answer document ranks *outside* first-stage `top_k` by cosine similarity (pushed down by lexically-
similar but semantically off-topic distractor documents) but *inside* a slightly larger candidate
pool, so a semantically-aware reranker (LLM reading full text, not just embedding similarity) can
promote it back into the final top-k. This was iterated empirically against the real local
`nomic-embed-text` embeddings until the vector-only baseline's precision@k on the fixture was
provably imperfect (see self-consistency.json for the actual numbers) — not asserted without
running it.

## 5. Local-only / dependency discipline confirmed

- No OpenRouter/Gemini call anywhere in this module or its test.
- No `.env` created or read.
- No new pip dependency added to `agents/pyproject.toml`; module imports only `agents/llm/client.py`
  (`LLMClient`) and `agents/eval/baselines/vector_rag.py` (already-shipped 5.2.1 code) plus stdlib.
- `agents/llm/ollama_client.py`'s `OllamaClient` (already exists, issue #18 subtask 3.4.1) is reused
  directly for the rerank LLM calls — this module does not talk to Ollama's HTTP API directly,
  unlike `vector_rag.py`'s embedding client (which had to, since `LLMClient` doesn't cover
  embeddings). This keeps "only `agents/llm/` calls provider APIs directly" architectural rule
  intact for the completion-shaped call.
