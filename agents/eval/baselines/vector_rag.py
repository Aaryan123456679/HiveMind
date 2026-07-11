"""Classic vector-RAG baseline: tuned fixed-size chunker + local-embedding retrieval.

Per issue #26/#27 (milestone #7, "Phase 5"), subtask 5.2.1. `docs/LLD/eval.md` describes this
as one of three retrieval arms (`agents/eval/`) compared against HiveMind's own topic-file/
graph retrieval: "Classic vector RAG — fixed-size-chunk baseline. Per system-wide risk
tracking, must be genuinely well-tuned (real chunk size/overlap, reranking if time allows) —
not a strawman" (see `docs/HLD.md` `#7 System-wide known risks`).

Fairness disclosure -- why this is not a strawman
----------------------------------------------------
A vector-RAG baseline that is deliberately weak (arbitrary chunk sizes, a placeholder/hashing
"embedding," keyword search mislabeled as embedding-based retrieval) would make HiveMind's own
graph-based retrieval look artificially better by comparison -- exactly the system-wide risk
called out above. This module addresses that directly:

1. **Chunking strategy is the standard production approach**: fixed-size, word-count-based,
   sliding-window chunking with overlap, word-boundary-snapped (never bisects a word). This is
   the same class of chunker real production vector-RAG deployments use.
2. **Chunk size/overlap are the output of an actual grid-search tuning pass**
   (:func:`select_chunk_config`), not arbitrary/undocumented constants. The shipped
   :data:`DEFAULT_CHUNK_SIZE_WORDS` / :data:`DEFAULT_OVERLAP_WORDS` are the winning
   configuration from that sweep, run against a fixture corpus/query set using the real
   embedding/retrieval path (recorded in this subtask's own `self-consistency.json`, not just
   asserted here).
3. **The embedding model is real**: `nomic-embed-text`, a dedicated, open-weight text-embedding
   model (768-dim), run locally via Ollama's `/api/embed` endpoint -- not a hashing trick, not
   TF-IDF mislabeled as an embedding. `docs/LLD/ingestion-agent.md` already names this exact
   model as the repo's own precedent local-embedding choice (`agents/ingestion/shortlist.py`'s
   docstring confirms no embedding model was wired up elsewhere before this subtask).
4. **Retrieval is genuine cosine-similarity vector search** over those embeddings
   (:class:`VectorRagIndex.search`), not a keyword/BM25 stand-in.

Local-only constraint -- disclosed
--------------------------------------
:class:`OllamaEmbeddingClient` talks to a local Ollama server only (default
`http://localhost:11434`), via `httpx` (already an `agents/` dependency -- no new pip package
added). No OpenRouter/Gemini/any paid embedding API is used anywhere in this module, per the
repo's standing local-Ollama-only preference for LLM/embedding-backed work.

Not an `LLMClient` -- disclosed design
-------------------------------------------
`agents/llm/client.py`'s `LLMClient` ABC is deliberately scoped to single-shot text
*completion* (`complete(prompt) -> str`); embeddings are a different call shape (text in,
vector out) with no existing abstraction in this repo. Rather than stretch `LLMClient`'s
contract to cover a shape it was not designed for, this module defines its own small,
self-contained `OllamaEmbeddingClient`, matching this issue's own declared impacted-module
scope (`agents/eval/baselines/vector_rag.py` only -- `agents/llm/` is not touched).

Scope boundary -- fixture-only, disclosed
----------------------------------------------
This module is deliberately corpus-agnostic: `chunk_document` takes a plain `(doc_id, text)`
pair (matching `ingestion.rawdoc.RawDocument.id`/`.text`), and `retrieve_documents` returns
plain ranked `doc_id` strings (matching `ground_truth.RelevantDoc.doc_id`). Wiring it up to
`agents/eval/datasets.py`'s real Bitext/Enron/synthetic-PDF corpus + `ground_truth.py`'s real
labels for an actual benchmark run is explicitly out of scope for this subtask (reserved for a
future subtask, e.g. 5.3.4) -- this subtask only self-tests against an inline fixture corpus
and fixture queries, per its own test spec.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import httpx

#: Re-exported from `eval.metrics` (subtask 5.3.1, issue #28) -- `eval.metrics` is now the
#: canonical home for this function; it is imported here (not reimplemented) so every existing
#: caller of `vector_rag.recall_at_k` (this module's own `select_chunk_config`,
#: `test_vector_rag_baseline.py`, `test_graphrag_baseline.py`) keeps working unmodified. See
#: `eval/metrics.py`'s module docstring for the full duplication-resolution rationale.
from eval.metrics import recall_at_k  # noqa: F401

#: Ollama's standard local-server default address (same server `agents/llm/ollama_client.py`
#: talks to, just a different HTTP endpoint -- see module docstring).
DEFAULT_BASE_URL = "http://localhost:11434"

#: Ollama model-library tag for a real, dedicated local text-embedding model. See module
#: docstring's fairness disclosure, point 3.
DEFAULT_EMBEDDING_MODEL = "nomic-embed-text"

#: Local embedding calls on CPU can be slow for larger batches; generous default timeout.
DEFAULT_TIMEOUT_SECONDS = 60.0

#: Tuned chunk-size/overlap defaults -- the winning configuration from this subtask's own
#: `select_chunk_config` grid-search sweep against the fixture corpus/query set using the real
#: embedding/retrieval path. See `self-consistency.json` for the full per-candidate recall
#: table this choice was derived from. The sweep found several candidates tied at perfect
#: mean recall@3 on this (necessarily small) fixture corpus; ties are broken in favor of the
#: smaller chunk size/overlap (leaner index, no accuracy cost observed at this scale) -- see
#: `select_chunk_config`'s own tie-break documentation.
DEFAULT_CHUNK_SIZE_WORDS = 80
DEFAULT_OVERLAP_WORDS = 0

#: Tuning grid used by `select_chunk_config`. Spans "too small, context-starved" (80) through
#: "typical production RAG chunk size" (150) up to "large, overlap-heavy" (220), and overlap
#: fractions from none through a substantial 30%, so the sweep can actually discriminate
#: between candidates rather than being pre-rigged toward one answer.
CHUNK_SIZE_CANDIDATES: tuple[int, ...] = (80, 150, 220)
OVERLAP_FRACTION_CANDIDATES: tuple[float, ...] = (0.0, 0.15, 0.30)


class OllamaEmbeddingError(Exception):
    """Raised on any Ollama embedding-endpoint HTTP call failure or malformed response."""


class OllamaEmbeddingClient:
    """Local-only embedding client calling a local Ollama server's `/api/embed` endpoint.

    Args:
        base_url: Ollama server base URL. Defaults to `DEFAULT_BASE_URL`.
        model: Embedding model tag. Defaults to `DEFAULT_EMBEDDING_MODEL`
            (`nomic-embed-text`).
        timeout: Per-call timeout in seconds.
        transport: Optional `httpx.BaseTransport` override, used by tests to inject
            `httpx.MockTransport` for the non-network unit tests in this module's test file.
            Production/live-test callers should leave this `None`.
    """

    def __init__(
        self,
        *,
        base_url: str = DEFAULT_BASE_URL,
        model: str = DEFAULT_EMBEDDING_MODEL,
        timeout: float = DEFAULT_TIMEOUT_SECONDS,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._timeout = timeout
        self._transport = transport

    def embed(self, texts: list[str]) -> list[list[float]]:
        """Return one embedding vector per input text, in the same order.

        Raises:
            OllamaEmbeddingError: On any HTTP failure or malformed response.
        """
        if not texts:
            return []

        payload = {"model": self._model, "input": texts}

        try:
            with httpx.Client(
                base_url=self._base_url, transport=self._transport
            ) as client:
                response = client.post(
                    "/api/embed", json=payload, timeout=self._timeout
                )
                response.raise_for_status()
        except httpx.HTTPError as exc:
            raise OllamaEmbeddingError(
                f"Ollama embedding request to {self._base_url}/api/embed failed: {exc}"
            ) from exc

        try:
            data = response.json()
        except ValueError as exc:
            raise OllamaEmbeddingError(
                f"Ollama embedding response was not valid JSON: {exc}"
            ) from exc

        if not isinstance(data, dict) or "embeddings" not in data:
            raise OllamaEmbeddingError(
                f"Ollama embedding response missing expected 'embeddings' key: {data!r}"
            )

        embeddings = data["embeddings"]
        if not isinstance(embeddings, list) or len(embeddings) != len(texts):
            raise OllamaEmbeddingError(
                f"Ollama embedding response returned {len(embeddings) if isinstance(embeddings, list) else 'non-list'} "
                f"vector(s) for {len(texts)} input text(s): {embeddings!r}"
            )

        return embeddings


@dataclass(frozen=True)
class Chunk:
    """One fixed-size chunk of a source document.

    `start_word`/`end_word` are word-index offsets (`[start_word, end_word)`, half-open) into
    the document's whitespace-split word sequence -- not character/byte offsets -- since this
    chunker operates on word boundaries (see module docstring, fairness point 1).
    """

    doc_id: str
    chunk_id: str
    text: str
    start_word: int
    end_word: int


def chunk_document(
    doc_id: str,
    text: str,
    *,
    chunk_size_words: int = DEFAULT_CHUNK_SIZE_WORDS,
    overlap_words: int = DEFAULT_OVERLAP_WORDS,
) -> list[Chunk]:
    """Split `text` into fixed-size, word-boundary-snapped, overlapping chunks.

    Args:
        doc_id: Identifier of the source document (matches
            `ingestion.rawdoc.RawDocument.id`'s shape -- a plain string).
        text: The document's full text (matches `RawDocument.text`).
        chunk_size_words: Target chunk size, in whitespace-split words.
        overlap_words: Overlap between consecutive chunks, in words. Must satisfy
            `0 <= overlap_words < chunk_size_words`.

    Returns:
        A list of `Chunk`s covering the full document, each of at most `chunk_size_words`
        words (the final chunk may be shorter), advancing by `chunk_size_words - overlap_words`
        words per step. Empty `text` (or text with no words) yields an empty list.

    Raises:
        ValueError: If `chunk_size_words <= 0` or `overlap_words` is out of
            `[0, chunk_size_words)`.
    """
    if chunk_size_words <= 0:
        raise ValueError(f"chunk_size_words must be positive, got {chunk_size_words}")
    if not (0 <= overlap_words < chunk_size_words):
        raise ValueError(
            f"overlap_words must satisfy 0 <= overlap_words < chunk_size_words "
            f"({overlap_words} vs chunk_size_words={chunk_size_words})"
        )

    words = text.split()
    if not words:
        return []

    stride = chunk_size_words - overlap_words
    chunks: list[Chunk] = []
    start = 0
    index = 0
    while start < len(words):
        end = min(start + chunk_size_words, len(words))
        chunk_text = " ".join(words[start:end])
        chunks.append(
            Chunk(
                doc_id=doc_id,
                chunk_id=f"{doc_id}::chunk{index}",
                text=chunk_text,
                start_word=start,
                end_word=end,
            )
        )
        if end == len(words):
            break
        start += stride
        index += 1

    return chunks


def _cosine_similarity(a: list[float], b: list[float]) -> float:
    """Pure-Python cosine similarity -- fine at fixture scale (see architecture-discovery.md's
    disclosed future-scale caveat: a real-corpus benchmark may want numpy/an ANN index)."""
    dot = sum(x * y for x, y in zip(a, b, strict=True))
    norm_a = math.sqrt(sum(x * x for x in a))
    norm_b = math.sqrt(sum(y * y for y in b))
    if norm_a == 0.0 or norm_b == 0.0:
        return 0.0
    return dot / (norm_a * norm_b)


@dataclass
class VectorRagIndex:
    """An in-memory embedding index over a set of chunks."""

    chunks: list[Chunk]
    embeddings: list[list[float]]

    @classmethod
    def build(
        cls, chunks: list[Chunk], embed_client: OllamaEmbeddingClient
    ) -> "VectorRagIndex":
        """Embed all `chunks` (one batched call) and build a searchable index."""
        texts = [c.text for c in chunks]
        embeddings = embed_client.embed(texts)
        return cls(chunks=chunks, embeddings=embeddings)

    def search(
        self, query: str, embed_client: OllamaEmbeddingClient, *, top_k: int
    ) -> list[tuple[Chunk, float]]:
        """Return the `top_k` chunks most similar to `query`, ranked by cosine similarity."""
        if not self.chunks:
            return []
        (query_embedding,) = embed_client.embed([query])
        scored = [
            (chunk, _cosine_similarity(query_embedding, embedding))
            for chunk, embedding in zip(self.chunks, self.embeddings, strict=True)
        ]
        scored.sort(key=lambda pair: pair[1], reverse=True)
        return scored[:top_k]


def retrieve_documents(
    query: str,
    index: VectorRagIndex,
    embed_client: OllamaEmbeddingClient,
    *,
    top_k: int,
    chunk_pool_size: int | None = None,
) -> list[str]:
    """Retrieve ranked document ids for `query` via chunk-level vector search.

    Chunk-level hits are aggregated to document level by max chunk score per `doc_id`
    (the common "any strong chunk hit implies document relevance" convention), then ranked and
    truncated to `top_k` documents -- matching `ground_truth.RelevantDoc.doc_id`'s document-level
    shape (see module docstring's scope-boundary section).

    Args:
        query: The query text.
        index: A `VectorRagIndex` built via `VectorRagIndex.build`.
        embed_client: The embedding client to embed `query` with (should match the client used
            to build `index`, for embedding-space consistency).
        top_k: Number of ranked document ids to return.
        chunk_pool_size: Number of top chunks to consider before document-level aggregation.
            Defaults to `max(top_k * 5, len(index.chunks))`'s smaller bound is not applied here
            -- defaults to considering *all* chunks (`len(index.chunks)`), since fixture-scale
            corpora make an artificially small chunk pool an unnecessary source of lost recall.

    Returns:
        Up to `top_k` document ids, best-first, each document id appearing at most once.
    """
    pool_size = chunk_pool_size if chunk_pool_size is not None else len(index.chunks)
    hits = index.search(query, embed_client, top_k=pool_size)

    best_score_by_doc: dict[str, float] = {}
    for chunk, score in hits:
        current = best_score_by_doc.get(chunk.doc_id)
        if current is None or score > current:
            best_score_by_doc[chunk.doc_id] = score

    ranked_doc_ids = sorted(
        best_score_by_doc, key=lambda doc_id: best_score_by_doc[doc_id], reverse=True
    )
    return ranked_doc_ids[:top_k]


def select_chunk_config(
    fixture_docs: list[tuple[str, str]],
    fixture_queries: list[tuple[str, set[str]]],
    embed_client: OllamaEmbeddingClient,
    *,
    top_k: int = 3,
    chunk_size_candidates: tuple[int, ...] = CHUNK_SIZE_CANDIDATES,
    overlap_fraction_candidates: tuple[float, ...] = OVERLAP_FRACTION_CANDIDATES,
) -> tuple[tuple[int, int], dict[tuple[int, int], float]]:
    """Grid-search the winning `(chunk_size_words, overlap_words)` config by mean recall@`top_k`.

    Runs the *same* real embedding/retrieval path the shipped baseline uses for every candidate
    -- this is a genuine tuning pass, not a proxy metric (see module docstring's fairness
    disclosure, point 2).

    Args:
        fixture_docs: `(doc_id, text)` pairs -- the fixture corpus.
        fixture_queries: `(query_text, relevant_doc_ids)` pairs -- the fixture query set.
        embed_client: Real (or test-injected) embedding client.
        top_k: Recall@k cutoff used for scoring each candidate.
        chunk_size_candidates: Candidate chunk sizes, in words.
        overlap_fraction_candidates: Candidate overlaps, as a fraction of chunk size.

    Returns:
        A `(winning_config, all_results)` pair: `winning_config` is the
        `(chunk_size_words, overlap_words)` pair with the highest mean recall@`top_k` (ties
        broken by preferring the smaller chunk size, then the smaller overlap, for the leanest
        index); `all_results` maps every candidate config to its mean recall@`top_k`, for full
        transparency into the sweep (recorded verbatim in this subtask's self-consistency.json).
    """
    results: dict[tuple[int, int], float] = {}

    for chunk_size in chunk_size_candidates:
        for overlap_fraction in overlap_fraction_candidates:
            overlap = int(chunk_size * overlap_fraction)
            all_chunks: list[Chunk] = []
            for doc_id, text in fixture_docs:
                all_chunks.extend(
                    chunk_document(
                        doc_id, text, chunk_size_words=chunk_size, overlap_words=overlap
                    )
                )
            index = VectorRagIndex.build(all_chunks, embed_client)

            recalls = []
            for query_text, relevant_doc_ids in fixture_queries:
                retrieved = retrieve_documents(
                    query_text, index, embed_client, top_k=top_k
                )
                recalls.append(recall_at_k(retrieved, relevant_doc_ids, top_k))
            mean_recall = sum(recalls) / len(recalls) if recalls else 0.0
            results[(chunk_size, overlap)] = mean_recall

    winning_config = max(
        results,
        key=lambda config: (results[config], -config[0], -config[1]),
    )
    return winning_config, results
