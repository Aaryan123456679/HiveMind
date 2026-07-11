"""Shared final-answer LLM call, wired identically across all three benchmark arms.

Per issue #27 (milestone #7, "Phase 5"), subtask 5.2.4 -- the final subtask of the benchmark
harness's retrieval-arm scope. `docs/LLD/eval.md` (and `docs/LLD/llm-provider.md`) already
*document* the intended design: "All three arms share an identical final-answer LLM ... so that
only the retrieval step varies between arms." This module makes that documented intent real and
enforced in code, rather than merely asserted in prose.

Reuse, not reinvention -- disclosed design
--------------------------------------------
HiveMind's own production final-answer call path already exists: `agents/query/synthesizer.py`'s
`synthesize_answer()` (issue #24, `docs/LLD/query-agent.md`'s "`synthesizer.py`" section --
"Final LLM call: refined intent + concatenated selected markdown (with file-path headers) ->
answer with inline file-path citations"). Per this subtask's own instruction ("reuse it if it
exists rather than inventing a parallel one"), this module does **not** define a second,
parallel final-answer implementation. It reuses `query.synthesizer.synthesize_answer` verbatim
and unmodified.

Enforcement mechanism -- how this is *provably* identical, not just documented
---------------------------------------------------------------------------------
`generate_final_answer()` below is the single function that performs final-answer generation.
Every one of the three per-arm wrapper functions (`run_hivemind_arm`, `run_vector_rag_arm`,
`run_graphrag_lite_arm`) calls this *one* function -- the same function object, at the same call
site pattern -- for its final-answer step, after performing that arm's own (and only that arm's
own) retrieval. This is a structural guarantee, not merely an observed coincidence of matching
call sites: a future edit that made one arm bypass `generate_final_answer()` (e.g. to call
`llm_client.complete()` directly with a different prompt, or a different model literal) would
require visibly modifying that arm's wrapper to *not* call the shared function -- a large,
reviewable diff -- rather than a silent, easy-to-miss divergence. `test_shared_final_llm.py`
additionally spies on both `generate_final_answer` and the underlying `LLMClient.complete()`
call to catch any such divergence even if it did happen.

`query_type` and `entities` are deliberately fixed literal constants inside
`generate_final_answer()` (`_EVAL_QUERY_TYPE`, `_EVAL_ENTITIES`), not per-call parameters --
letting a caller vary them per-arm would reopen exactly the per-arm prompt-divergence risk this
subtask exists to close.

Ollama-only -- disclosed constraint
--------------------------------------
This module never constructs a provider client itself; callers construct
`llm.ollama_client.OllamaClient` directly and pass it in as `llm_client` (mirroring every prior
eval subtask's -- 5.1.2/5.1.3/5.2.1/5.2.2/5.2.3 -- own precedent). No `.env` file is read, no
OpenRouter/Gemini client is imported or constructed anywhere in this module.

Corpus/selected-markdown shape -- disclosed design
------------------------------------------------------
`synthesize_answer()` expects `selected_markdown` to be a single string with each section
preceded by a `## File: <path>` header (see `synthesizer.py`'s own docstring). This module's
`_build_selected_markdown()` renders exactly that format from a plain `{doc_id: text}` corpus
mapping plus a ranked `list[str]` of retrieved doc ids -- the exact output shape all three
retrieval arms (`vector_rag.retrieve_documents`, `vector_rag_rerank
.retrieve_documents_reranked`, `graphrag_lite.retrieve_documents`) already share (see each
module's own "output-shape consistency" disclosure). Any retrieved id absent from `corpus` is
silently skipped (not raised) -- a retrieval arm's id list is not guaranteed to be a corpus
superset in a small fixture test, and only rendering what is actually resolvable mirrors
`synthesizer.py`'s own "headers reflect what's actually present" convention.

HiveMind arm -- scope boundary, disclosed
---------------------------------------------
HiveMind's own real retrieval already exists end-to-end in `agents/query/pipeline.py`'s
`run_query_pipeline()` (gRPC-backed against the Go engine; issue #25/#56). Re-invoking that
full gRPC-backed pipeline from here is out of this subtask's scope (impacted modules are
`agents/eval/pipeline.py` and its test only). `run_hivemind_arm()` therefore accepts an
already-retrieved `retrieved_doc_ids` list as an explicit parameter -- documented as such -- so
a future benchmark-run subtask can drive it from `run_query_pipeline()`'s real retrieval output
(or, before that wiring lands, a fixture/stand-in list), while this subtask still forces its
final-answer step through the identical shared function today.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from typing import TYPE_CHECKING

from eval.baselines.graphrag_lite import DEFAULT_MAX_HOPS
from eval.baselines import graphrag_lite, vector_rag
from query.synthesizer import SynthesizerResult, synthesize_answer

if TYPE_CHECKING:
    from eval.baselines.graphrag_lite import EntityGraph
    from eval.baselines.vector_rag import OllamaEmbeddingClient, VectorRagIndex
    from llm.client import LLMClient

#: Fixed literal `query_type` passed to `synthesize_answer` for every eval-benchmark
#: final-answer call, for all three arms alike. See module docstring's "deliberately fixed
#: literal constants" disclosure.
_EVAL_QUERY_TYPE = "eval_benchmark"

#: Fixed literal `entities` passed to `synthesize_answer` for every eval-benchmark final-answer
#: call. Eval baselines have no `intent_refiner`-produced entity list (that is a HiveMind-arm-
#: only production concept); an empty tuple keeps this argument identical across all three arms
#: rather than populating it only for some.
_EVAL_ENTITIES: tuple[str, ...] = ()


def _build_selected_markdown(
    retrieved_doc_ids: Sequence[str], corpus: Mapping[str, str]
) -> str:
    """Render `retrieved_doc_ids` (ranked, best-first) into the `## File: <doc_id>`-headered
    markdown blob `query.synthesizer.synthesize_answer` expects as `selected_markdown`.

    Any id in `retrieved_doc_ids` absent from `corpus` is silently skipped (see module
    docstring's "corpus/selected-markdown shape" disclosure).
    """
    sections = []
    for doc_id in retrieved_doc_ids:
        text = corpus.get(doc_id)
        if text is None:
            continue
        sections.append(f"## File: {doc_id}\n\n{text}")
    return "\n\n".join(sections)


def generate_final_answer(
    query: str,
    retrieved_doc_ids: Sequence[str],
    corpus: Mapping[str, str],
    llm_client: "LLMClient",
    *,
    model: str | None = None,
    temperature: float = 0.0,
    max_tokens: int | None = None,
    timeout: float | None = None,
) -> SynthesizerResult:
    """The ONE shared final-answer generation function -- see module docstring's "Enforcement
    mechanism" section. Every retrieval arm's final answer is produced by calling this exact
    function; only `retrieved_doc_ids` (and therefore `selected_markdown`) legitimately differs
    between arms.

    Args:
        query: The raw benchmark query text. Passed as `synthesize_answer`'s `refined_intent`
            positional argument -- eval baselines have no separate intent-refinement step, so
            the raw query fills that role identically for all three arms.
        retrieved_doc_ids: That arm's own ranked `list[str]` of retrieved document ids (the
            shared output shape all three retrieval arms already produce).
        corpus: `{doc_id: text}` mapping resolving `retrieved_doc_ids` to their content.
        llm_client: The `LLMClient` used for the completion call (an `OllamaClient` instance in
            every real caller, per the Ollama-only constraint).
        model, temperature, max_tokens, timeout: Forwarded verbatim to `synthesize_answer` (and
            from there, verbatim to `llm_client.complete()`) -- identical for every caller of
            this function, since every arm-runner wrapper below forwards its own same-named
            parameters through unchanged.

    Returns:
        The `SynthesizerResult` from `query.synthesizer.synthesize_answer`.
    """
    selected_markdown = _build_selected_markdown(retrieved_doc_ids, corpus)
    return synthesize_answer(
        query,
        _EVAL_QUERY_TYPE,
        _EVAL_ENTITIES,
        selected_markdown,
        llm_client,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=timeout,
    )


def run_hivemind_arm(
    query: str,
    retrieved_doc_ids: Sequence[str],
    corpus: Mapping[str, str],
    llm_client: "LLMClient",
    *,
    model: str | None = None,
) -> SynthesizerResult:
    """HiveMind arm: retrieval already performed elsewhere (see module docstring's "HiveMind
    arm -- scope boundary" disclosure); this wrapper only forces the final-answer step through
    the same shared `generate_final_answer` every other arm uses.
    """
    return generate_final_answer(query, retrieved_doc_ids, corpus, llm_client, model=model)


def run_vector_rag_arm(
    query: str,
    index: "VectorRagIndex",
    embed_client: "OllamaEmbeddingClient",
    corpus: Mapping[str, str],
    llm_client: "LLMClient",
    *,
    top_k: int,
    model: str | None = None,
) -> SynthesizerResult:
    """Classic vector-RAG arm (5.2.1): retrieve via `vector_rag.retrieve_documents`, then
    generate the final answer via the same shared `generate_final_answer` every other arm uses.
    """
    retrieved_doc_ids = vector_rag.retrieve_documents(query, index, embed_client, top_k=top_k)
    return generate_final_answer(query, retrieved_doc_ids, corpus, llm_client, model=model)


def run_graphrag_lite_arm(
    query: str,
    graph: "EntityGraph",
    corpus: Mapping[str, str],
    llm_client: "LLMClient",
    *,
    top_k: int,
    max_hops: int = DEFAULT_MAX_HOPS,
    model: str | None = None,
) -> SynthesizerResult:
    """GraphRAG-lite arm (5.2.3): retrieve via `graphrag_lite.retrieve_documents`, then
    generate the final answer via the same shared `generate_final_answer` every other arm uses.
    """
    retrieved_doc_ids = graphrag_lite.retrieve_documents(
        query, graph, llm_client, top_k=top_k, max_hops=max_hops, model=model
    )
    return generate_final_answer(query, retrieved_doc_ids, corpus, llm_client, model=model)
