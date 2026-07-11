# Architecture Discovery — Subtask 5.2.1

## Existing surfaces read (index-first, per token order)
- `.cdr/memory/pending.md` — OQ-1/OQ-2 resolution history (5.1.1-5.1.3), local-Ollama-only
  standing preference, explicit no-live-benchmark-execution constraint with the carve-out for
  5.2.1-5.2.4 fixture/local-based code.
- `docs/HLD.md` — `#7 System-wide known risks`: "vector-RAG baseline must be genuinely
  well-tuned (real chunk size/overlap), else HiveMind's graph-based approach 'wins' by
  comparing against a strawman." This is the load-bearing fairness constraint for this subtask.
- `docs/LLD/eval.md` — Benchmark harness LLD. Confirms 3 retrieval arms (HiveMind, classic
  vector RAG, simplified GraphRAG-style) must share an identical corpus and identical
  final-answer LLM, with only the retrieval step varying. Classic vector-RAG arm description:
  "fixed-size-chunk baseline ... genuinely well-tuned (real chunk size/overlap, reranking if
  time allows) — not a strawman."
- `agents/eval/datasets.py` (5.1.1) — common dataset interface; yields
  `ingestion.rawdoc.RawDocument` (`id`, `source_type`, `text`, `structured_fields`,
  `timestamp`). This baseline's chunker input type is `RawDocument`, matching this shape so a
  future subtask can feed it `load_dataset(...)` output directly with zero adapter code.
- `agents/eval/ground_truth.py` (5.1.3) — `TopicGroundTruth`/`QueryLabel`/`RelevantDoc` (doc-id
  + `"primary"`/`"cross_reference"` label). Ground truth is **document-level**, not
  chunk-level — the corpus's chunker/retriever internals are free to choose their own chunk
  granularity, but this baseline's public retrieval API must ultimately return ranked
  **document ids** (aggregated from chunk hits) to be scoreable against this ground-truth shape
  in a future combined-benchmark subtask (5.3.4). Confirmed no chunk-level ground truth exists
  or is planned — designing for one would be speculative.
- `agents/llm/client.py` / `agents/llm/ollama_client.py` — provider-agnostic `LLMClient` ABC,
  `OllamaClient` implementation. Scoped to single-shot **text completion**
  (`complete(prompt) -> str`), not embeddings — there is no existing embedding client anywhere
  in the repo. `agents/ingestion/shortlist.py`'s own docstring explicitly confirms this: "no
  embedding model is wired up anywhere else" (as of 3.4.2, still true today — confirmed via
  fresh repo-wide grep for `embed`/`nomic`/`sentence-transformers` before writing this doc).
- `agents/pyproject.toml` — dependencies: `fastapi`, `uvicorn`, `grpcio(+tools)`, `protobuf`,
  `pydantic`, `httpx`, `pymupdf`. No `numpy`, no `sentence-transformers`, no vector-similarity
  library of any kind.
- `docs/LLD/ingestion-agent.md` (per `shortlist.py`'s own citation) explicitly names "a local
  embedding model, e.g. `nomic-embed-text`" as the repo's own precedent choice for "cheap
  embedding pre-filter," even though 3.4.2 ultimately chose BM25 instead (dependency-light,
  sufficient for that subtask's bounded-pool re-ranking need). This subtask's need is different
  — the issue explicitly requires "an embedding-based retrieval step," so BM25-only is not an
  option here; `nomic-embed-text` is the repo's own pre-existing, documented intended choice
  and is what I use.
- Confirmed via `ollama list` in this sandbox that `nomic-embed-text` is not pre-pulled but
  `ollama pull nomic-embed-text` succeeds (274MB, local, no API key, no network egress to any
  paid provider) and `POST /api/embed` returns real 768-dim vectors for local text. This is the
  same local Ollama server already used by `OllamaClient`, just a different HTTP endpoint
  (`/api/embed` vs `/api/generate`).

## Design decisions

### 1. Embedding model: local Ollama `nomic-embed-text`, not a paid API, not a new pip dependency
- Satisfies the hard "no OpenRouter/Gemini/paid API" constraint and the "check existing
  dependencies before adding a new one" instruction: `httpx` (already a dependency) is
  sufficient to call Ollama's `/api/embed` HTTP endpoint directly — no `sentence-transformers`,
  no `numpy`, no new pip package needed at all.
- `nomic-embed-text` is a real, purpose-built, widely-used open-weight text-embedding model
  (768-dim, ~137M params) — not a hash-based or bag-of-words stand-in. This matters directly
  for the fairness concern: an embedding-based retrieval step built on a deliberately weak
  "embedding" (e.g. random hashing, raw TF-IDF vectors mislabeled as embeddings) would itself be
  a strawman even if the chunking were well-tuned. Using a real, dedicated embedding model closes
  that half of the fairness gap.
- Implemented as a small, self-contained `OllamaEmbeddingClient` inside
  `agents/eval/baselines/vector_rag.py` (not added to `agents/llm/`), matching the issue's own
  declared impacted-module scope (`agents/eval/baselines/vector_rag.py`,
  `agents/eval/test_vector_rag_baseline.py` only) — this avoids widening
  `agents/llm/`'s existing `LLMClient` completion-only ABC contract (whose docstring explicitly
  scopes it to `complete()` only, by design) for a need specific to this one baseline.

### 2. Chunker: fixed-size, word-count-based, sentence-boundary-snapped, with a real
   tuning pass — not an arbitrary default
- **Fixed-size** per the issue's explicit "fixed-size-chunk baseline" wording — this is
  deliberately the classic/naive strategy (vs. e.g. semantic chunking), because the benchmark's
  entire point is comparing HiveMind's topic-aware store against the standard, ubiquitous
  vector-RAG chunking strategy actually used in production RAG systems. Fixed-size is *correct*
  as the strategy, not sloppy — the fairness risk is in **how well tuned** the size/overlap are,
  not in choosing fixed-size as the strategy at all.
- Chunk boundaries snap to whitespace/word boundaries (never mid-word), a minimal but real
  quality safeguard against the most naive (and genuinely strawman) implementation of "fixed
  size" — literal character-count slicing that can bisect a word or sentence mid-token, actively
  degrading embedding quality for no benefit. This costs nothing in complexity and removes an
  easy, gratuitous source of unfairness.
- **Actual tuning, not just documented defaults**: this subtask implements
  `select_chunk_config()`, a small grid-search helper that evaluates a handful of candidate
  `(chunk_size_words, overlap_words)` pairs against a held-out fixture validation query set
  (recall@k), using the *same* real local-embedding retrieval path the shipped baseline uses —
  not a proxy metric. The shipped module's `DEFAULT_CHUNK_SIZE_WORDS`/`DEFAULT_OVERLAP_WORDS`
  constants are the winning configuration from that sweep, run once during this subtask's own
  self-consistency step and recorded there (exact sweep results, not just final numbers) so the
  choice is falsifiable/reproducible rather than asserted. Candidate grid: chunk sizes
  {80, 150, 220} words × overlaps {0%, 15%, 30% of chunk size} — spanning from "too small,
  context-starved" through "typical production RAG chunk size" up to "large, overlap-heavy"
  so the sweep can actually discriminate rather than being pre-rigged toward one answer.
- Overlap is expressed as a fraction of chunk size (not a fixed word count) so the grid remains
  meaningful across different chunk sizes.

### 3. Retrieval: real cosine-similarity vector search, document-level aggregation
- `VectorRagIndex.search(query, top_k)` embeds the query via the same `OllamaEmbeddingClient`
  and ranks all indexed chunks by cosine similarity — genuine vector search, not a keyword
  fallback disguised as one. Implemented in pure Python (dot product / L2 norms) since the
  fixture-scale corpus this subtask ships does not warrant a new numeric-library dependency;
  a future subtask running this baseline at real-corpus scale may reasonably choose to swap in
  `numpy`/a vector index (e.g. via an ANN library) without changing this module's public
  chunk/embed/search API shape.
- `retrieve_documents(query, index, top_k)` aggregates chunk-level scores to document-level
  ranking (max chunk score per `doc_id`, matching the common "any strong chunk hit implies
  document relevance" convention used across the IR literature for chunk-then-aggregate
  systems), producing ranked document ids directly comparable against `ground_truth.py`'s
  document-level `RelevantDoc` judgments for a future combined-benchmark subtask.

## Why this is a fair baseline, not a strawman (explicit statement)
1. **Chunking strategy** is the standard production approach (fixed-size with overlap), not a
   deliberately degenerate one (e.g. whole-document-as-one-chunk, which would starve retrieval
   granularity; or 1-sentence chunks, which would starve context).
2. **Chunk size/overlap are the output of an actual grid-search tuning pass** on real recall
   data using the real embedding/retrieval path, not arbitrary/undocumented constants.
3. **The embedding model is a real, dedicated, open-weight text-embedding model**
   (`nomic-embed-text`, 768-dim) run locally via Ollama — the same class of model a genuine
   production vector-RAG deployment would use — not a hashing trick or degenerate placeholder.
4. **Retrieval is genuine cosine-similarity vector search** over those embeddings, not a
   keyword/BM25 stand-in mislabeled as "embedding-based."
5. **Scope is honestly bounded**: this subtask does not add a reranking step (the LLD's own
   wording treats it as optional, "if time allows," not required), and does not run against the
   real corpus — both are explicit, disclosed scope boundaries, not silent omissions that would
   understate the baseline's true competitiveness.

## Impact analysis (see also `impact-analysis.json`)
- New files only: `agents/eval/baselines/__init__.py`, `agents/eval/baselines/vector_rag.py`,
  `agents/eval/test_vector_rag_baseline.py`. No existing file is modified.
- No changes to `agents/llm/`, `agents/eval/datasets.py`, `agents/eval/ground_truth.py`,
  `data/`, `engine/`, or any proto/wire contract.
- New optional runtime dependency: none (uses existing `httpx`). New optional *local model*
  dependency: `nomic-embed-text` pulled via `ollama pull nomic-embed-text` — not a pip
  dependency, and the module/tests fail closed (skip, per the repo's own established
  `test_e2e_smoke.py` skip-if-unreachable convention) if it is not present, rather than
  silently falling back to a degraded/mocked path.
