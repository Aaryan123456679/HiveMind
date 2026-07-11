"""Optional reranking stage for the vector-RAG baseline (issue #27, subtask 5.2.2).

`docs/HLD.md` #7 and `docs/LLD/eval.md` describe reranking as an optional *addition* to the same
classic-vector-RAG arm shipped in subtask 5.2.1 (`agents/eval/baselines/vector_rag.py`) --
"real chunk size/overlap, reranking if time allows" -- not a separate retrieval arm. This module
therefore reuses 5.2.1's `chunk_document` / `OllamaEmbeddingClient` / `VectorRagIndex` /
`retrieve_documents` unmodified for first-stage retrieval and adds only a second-stage reranker on
top, toggleable via `retrieve_documents_reranked(..., rerank=True|False)`. See this subtask's own
`architecture-discovery.md` for the full disclosed reasoning summarized below.

Why a candidate pool larger than `top_k` is required -- disclosed design
-------------------------------------------------------------------------
Precision@k and recall@k are both set-membership metrics over the top-k slice: reordering the
members *already inside* a fixed top-k list changes neither. For reranking to have any chance of
"measurably improving precision@k," it must be able to change *which* documents end up inside the
final top-k -- so this module always retrieves a `candidate_pool_size >= top_k` pool via 5.2.1's
`retrieve_documents`, and only truncates to `top_k` *after* the (optional) rerank step. When
`rerank=False`, `candidate_pool_size` defaults to `top_k` itself, making the non-reranked path
mathematically identical to calling `retrieve_documents(top_k=top_k)` directly (see this module's
own test file's equivalence test).

Cross-encoder model vs. Ollama-LLM-based reranking -- disclosed choice
-------------------------------------------------------------------------
`agents/pyproject.toml` has no `torch`/`sentence-transformers`/any ML-model dependency. A real
cross-encoder reranker (e.g. `cross-encoder/ms-marco-MiniLM-L-6-v2`) would require adding one --
a heavyweight new dependency for one explicitly "time-permitting" step, cutting against 5.2.1's own
disclosed "reuse existing httpx, no new embedding library" precedent and the repo's Ollama-only
standing preference for LLM-backed work. This module instead reranks by prompting the already-
running local Ollama LLM (via `agents/llm/client.py`'s existing `LLMClient` interface -- no new
abstraction needed, since prompted text-in/text-out completion is exactly `LLMClient.complete`'s
designed shape, unlike embeddings which needed 5.2.1's own bespoke client) to produce a listwise
ranking of a small candidate-document list. Zero new pip dependencies; fully local; no paid API;
no `.env`. The acceptance criterion's own wording ("a reranking step (e.g. cross-encoder)") treats
cross-encoder as one example, not a mandate.

Fixture design -- disclosed
-------------------------------
5.2.1's fixture corpus/queries were tuned so plain vector search already achieves near-perfect
recall@3 -- deliberately, since 5.2.1's job was proving the baseline itself well-tuned, not proving
reranking helps. Reusing it here would risk reranking having nothing to fix. This module's test
file therefore ships its own dedicated fixture, engineered so a lexically-similar-but-off-topic
distractor document outranks the true relevant document within first-stage vector search but is
recoverable within a slightly larger candidate pool by a reranker capable of reading full text.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

from eval.baselines.vector_rag import OllamaEmbeddingClient, VectorRagIndex, retrieve_documents
from llm.client import LLMClient

#: Default extra candidates pulled beyond `top_k` when reranking is enabled, before the reranker
#: narrows back down to `top_k`. Small and fixed at fixture scale; a real-corpus benchmark run
#: (out of this subtask's fixture-only scope, see module docstring) would likely want a larger,
#: tunable pool.
DEFAULT_EXTRA_CANDIDATES = 2

#: Per-candidate document text is truncated to this many characters in the rerank prompt, to keep
#: the prompt small and fast on local CPU-bound generation. Fixture-scale documents are short
#: enough that this rarely truncates in practice.
MAX_CANDIDATE_CHARS_IN_PROMPT = 600


def precision_at_k(retrieved_doc_ids: list[str], relevant_doc_ids: set[str], k: int) -> float:
    """Fraction of the top `k` of `retrieved_doc_ids` that are in `relevant_doc_ids`.

    Returns `1.0` if `k <= 0` (vacuously satisfied -- no slots to get wrong), matching
    `vector_rag.recall_at_k`'s vacuous-case convention style but for the precision denominator.
    """
    if k <= 0:
        return 1.0
    top_k_ids = retrieved_doc_ids[:k]
    if not top_k_ids:
        return 0.0
    hits = sum(1 for doc_id in top_k_ids if doc_id in relevant_doc_ids)
    return hits / len(top_k_ids)


@dataclass(frozen=True)
class RerankCandidate:
    """One candidate document's text, for reranking purposes.

    Fixture-scale: callers pass the whole document text (not just the best-matching chunk), since
    the fixture corpus documents are short and a reranker benefits from full context.
    """

    doc_id: str
    text: str


def build_rerank_prompt(query: str, candidates: list[RerankCandidate]) -> str:
    """Build a single listwise reranking prompt for `query` over `candidates`.

    Instructs the model to output an ordered, comma-separated list of candidate numbers
    (best-match first) and nothing else. Candidate texts are truncated to
    `MAX_CANDIDATE_CHARS_IN_PROMPT` characters to keep the prompt small.
    """
    lines = [
        "You are ranking candidate documents by relevance to a search query.",
        f"Query: {query}",
        "",
        "Candidates:",
    ]
    for i, candidate in enumerate(candidates, start=1):
        snippet = candidate.text[:MAX_CANDIDATE_CHARS_IN_PROMPT]
        lines.append(f"[{i}] {snippet}")
    lines.extend(
        [
            "",
            f"Rank all {len(candidates)} candidates from most relevant to least relevant to the "
            "query. Respond with ONLY a comma-separated list of the candidate numbers in ranked "
            f"order (most relevant first), e.g. \"{', '.join(str(i) for i in range(len(candidates), 0, -1))}\". "
            "Do not include any other text.",
        ]
    )
    return "\n".join(lines)


def parse_rerank_order(response_text: str, num_candidates: int) -> list[int]:
    """Parse an LLM's free-form rerank response into a full permutation of `1..num_candidates`.

    Extracts integers from `response_text` in order of first appearance, keeps only those within
    `[1, num_candidates]`, de-duplicates (keeping the first occurrence), then appends any missing
    candidate numbers (in their original `1..num_candidates` order) at the end. This guarantees a
    full permutation is always returned -- never drops or duplicates a candidate -- even if the
    model's output is malformed, partial, or includes extra commentary.
    """
    found = [int(match) for match in re.findall(r"\d+", response_text)]
    ordered: list[int] = []
    seen: set[int] = set()
    for n in found:
        if 1 <= n <= num_candidates and n not in seen:
            ordered.append(n)
            seen.add(n)
    for n in range(1, num_candidates + 1):
        if n not in seen:
            ordered.append(n)
    return ordered


def rerank_documents(
    query: str,
    candidates: list[RerankCandidate],
    llm_client: LLMClient,
    *,
    model: str | None = None,
) -> list[str]:
    """Rerank `candidates` for `query` using `llm_client`; returns doc ids best-first.

    Always returns a permutation of every input candidate's `doc_id` (see
    `parse_rerank_order`'s no-drop guarantee) -- reranking narrows/reorders, it never loses a
    candidate before the caller truncates to `top_k`.
    """
    if not candidates:
        return []
    prompt = build_rerank_prompt(query, candidates)
    response = llm_client.complete(prompt, model=model, temperature=0.0)
    order = parse_rerank_order(response, len(candidates))
    return [candidates[i - 1].doc_id for i in order]


def retrieve_documents_reranked(
    query: str,
    index: VectorRagIndex,
    embed_client: OllamaEmbeddingClient,
    *,
    top_k: int,
    rerank: bool,
    llm_client: LLMClient | None = None,
    llm_model: str | None = None,
    candidate_pool_size: int | None = None,
    doc_texts: dict[str, str] | None = None,
) -> list[str]:
    """Retrieve ranked document ids for `query`, optionally reranked by an LLM second stage.

    First stage is always 5.2.1's unmodified `retrieve_documents` (cosine-similarity vector
    search). When `rerank=False`, this call is equivalent to
    `retrieve_documents(query, index, embed_client, top_k=top_k)` (see module docstring's
    candidate-pool disclosure). When `rerank=True`, a larger candidate pool
    (`candidate_pool_size`, default `top_k + DEFAULT_EXTRA_CANDIDATES`) is retrieved first, then
    reranked via `rerank_documents` before truncating to `top_k`.

    Args:
        top_k: Final number of document ids to return.
        rerank: Whether to apply the LLM reranking second stage.
        llm_client: Required when `rerank=True`; the `LLMClient` used to rerank.
        llm_model: Optional model override passed to `llm_client.complete`.
        candidate_pool_size: First-stage candidate pool size. Defaults to `top_k` when
            `rerank=False`, or `top_k + DEFAULT_EXTRA_CANDIDATES` when `rerank=True`.
        doc_texts: Required when `rerank=True`; maps every candidate doc id that first-stage
            retrieval might return to its full text, for the rerank prompt.

    Raises:
        ValueError: If `rerank=True` and `llm_client`/`doc_texts` is omitted, or `doc_texts` is
            missing an entry for a retrieved candidate doc id.
    """
    if rerank:
        if llm_client is None:
            raise ValueError("llm_client is required when rerank=True")
        if doc_texts is None:
            raise ValueError("doc_texts is required when rerank=True")
        pool_size = (
            candidate_pool_size
            if candidate_pool_size is not None
            else top_k + DEFAULT_EXTRA_CANDIDATES
        )
    else:
        pool_size = candidate_pool_size if candidate_pool_size is not None else top_k

    first_stage = retrieve_documents(query, index, embed_client, top_k=pool_size)

    if not rerank:
        return first_stage[:top_k]

    candidates = []
    for doc_id in first_stage:
        if doc_id not in doc_texts:
            raise ValueError(f"doc_texts is missing an entry for retrieved doc id {doc_id!r}")
        candidates.append(RerankCandidate(doc_id=doc_id, text=doc_texts[doc_id]))

    reranked = rerank_documents(query, candidates, llm_client, model=llm_model)
    return reranked[:top_k]
