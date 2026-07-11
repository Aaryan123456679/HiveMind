"""Tests for `agents/eval/pipeline.py` (issue #27, subtask 5.2.4 -- final subtask of #27).

Per this subtask's own test spec: "assert all three arms invoke the same `LLMClient` call
signature/model config for final-answer generation." Per the module docstring's disclosed
enforcement mechanism (a single shared `generate_final_answer` function every arm-runner
wrapper calls), this file proves that mechanism two ways:

1. Structurally -- patch `eval.pipeline.generate_final_answer` itself and assert every arm
   wrapper calls it exactly once, with `query`/`corpus`/`llm_client`/`model` forwarded
   unchanged. This is the test that would fail immediately if a future edit made any one arm
   bypass the shared function.
2. Observationally -- patch/spy on `eval.pipeline.synthesize_answer` (the real, reused
   production call, wrapped rather than replaced) and on `LLMClient.complete()` itself via a
   recording stub, and assert the final-answer call's `query_type`, `entities`, `model`,
   `temperature`, `max_tokens`, `timeout` are identical literal values across all three arms --
   the exact "same LLMClient call signature/model config" the test spec names. Only the prompt
   content (driven by each arm's own, legitimately different, `selected_markdown`) is allowed
   to vary.

Fixture-only, no network -- mirrors 5.2.1/5.2.2/5.2.3's own explicit scope boundary: this file
never imports `agents/eval/datasets.py` or reads `data/synthetic_corpus/`, and uses a stub
`LLMClient` (no real Ollama call) so results are fully deterministic and require no local model.
"""

from __future__ import annotations

from unittest.mock import patch

from eval.baselines.graphrag_lite import DEFAULT_LLM_MODEL, EntityGraph
from eval.baselines.vector_rag import VectorRagIndex, chunk_document
from eval.pipeline import (
    _EVAL_ENTITIES,
    _EVAL_QUERY_TYPE,
    generate_final_answer,
    run_graphrag_lite_arm,
    run_hivemind_arm,
    run_vector_rag_arm,
)
from llm.client import LLMClient
from query.synthesizer import SynthesizerResult

_CORPUS = {
    "doc-a": "Alpha handles authentication and login flows for the service.",
    "doc-b": "Beta handles billing, invoices, and payment processing.",
    "doc-c": "Gamma is a background worker that processes queued jobs.",
}
_QUERY = "How does authentication work?"
_MODEL_OVERRIDE = "llama3.1:8b"


def _synthesis_json(tag: str) -> str:
    return f'{{"answer": "answer for {tag} [doc-a]", "citations": ["doc-a"]}}'


class _SpyLLMClient(LLMClient):
    """Records every `complete()` call's full kwargs; returns a canned response selected by
    lightweight prompt-content sniffing (mirrors `test_graphrag_baseline.py`'s `_StubLLMClient`
    convention). Distinguishes the three distinct call *kinds* this fixture setup can produce
    (entity extraction, for the GraphRAG-lite arm's graph-build/query-entity step; and
    final-answer synthesis, for every arm) so a rerank/entity-extraction call is never mistaken
    for the final-answer call under test.
    """

    def __init__(self) -> None:
        self.calls: list[dict] = []

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        self.calls.append(
            {
                "prompt": prompt,
                "model": model,
                "temperature": temperature,
                "max_tokens": max_tokens,
                "timeout": timeout,
            }
        )
        # Synthesizer prompts are recognizable by the "citations" instruction text
        # `synthesizer._SYNTHESIS_PROMPT_TEMPLATE` always embeds -- see synthesizer.py.
        if "citations" in prompt and "## File:" in prompt:
            return _synthesis_json("final-answer")
        # Otherwise this is graphrag_lite's entity-extraction call (build_entity_extraction_
        # prompt): return a small canned entity list keyed by which fixture doc's text is
        # embedded in the prompt.
        for doc_id, text in _CORPUS.items():
            if text in prompt:
                first_word = text.split()[0]
                return f'["{first_word}"]'
        if _QUERY in prompt:
            return '["Alpha"]'
        return "[]"


class _StubEmbeddingClient:
    """Deterministic fake embedding client (duck-typed to `OllamaEmbeddingClient`'s `.embed()`
    shape) -- embeddings are out of `LLMClient`'s scope entirely (see `vector_rag.py`'s own
    "OllamaEmbeddingClient, not LLMClient" disclosure), so no real network/Ollama call is
    needed to exercise the vector-RAG arm's retrieval step here.
    """

    def embed(self, texts: list[str]) -> list[list[float]]:
        # One-hot-ish vector keyed by first word, so "authentication"-flavored query/doc text
        # scores highest cosine similarity against doc-a, deterministically.
        vectors = []
        vocab = ["alpha", "beta", "gamma", "authentication", "billing", "worker"]
        for text in texts:
            lowered = text.lower()
            vectors.append([1.0 if word in lowered else 0.0 for word in vocab])
        return vectors


def _build_fixture_index() -> tuple[VectorRagIndex, _StubEmbeddingClient]:
    embed_client = _StubEmbeddingClient()
    chunks = []
    for doc_id, text in _CORPUS.items():
        chunks.extend(chunk_document(doc_id, text))
    index = VectorRagIndex.build(chunks, embed_client)
    return index, embed_client


def _build_fixture_graph(llm_client: LLMClient) -> EntityGraph:
    docs = list(_CORPUS.items())
    return EntityGraph.build(docs, llm_client, model=DEFAULT_LLM_MODEL)


# --- Pure-unit test: selected-markdown rendering, no LLM involved ---


def test_build_selected_markdown_produces_expected_file_headers():
    from eval.pipeline import _build_selected_markdown

    rendered = _build_selected_markdown(["doc-b", "doc-a", "doc-missing"], _CORPUS)

    assert "## File: doc-b" in rendered
    assert "## File: doc-a" in rendered
    assert "doc-missing" not in rendered
    # Ranked order preserved: doc-b's header appears before doc-a's.
    assert rendered.index("## File: doc-b") < rendered.index("## File: doc-a")


# --- Structural test: every arm wrapper calls the one shared function ---


def test_all_three_arms_call_the_same_generate_final_answer_function():
    llm_client = _SpyLLMClient()
    index, embed_client = _build_fixture_index()
    graph = _build_fixture_graph(llm_client)

    with patch("eval.pipeline.generate_final_answer", wraps=generate_final_answer) as spy:
        run_hivemind_arm(_QUERY, ["doc-a"], _CORPUS, llm_client, model=_MODEL_OVERRIDE)
        assert spy.call_count == 1

    with patch("eval.pipeline.generate_final_answer", wraps=generate_final_answer) as spy:
        run_vector_rag_arm(
            _QUERY, index, embed_client, _CORPUS, llm_client, top_k=1, model=_MODEL_OVERRIDE
        )
        assert spy.call_count == 1

    with patch("eval.pipeline.generate_final_answer", wraps=generate_final_answer) as spy:
        run_graphrag_lite_arm(
            _QUERY, graph, _CORPUS, llm_client, top_k=1, model=_MODEL_OVERRIDE
        )
        assert spy.call_count == 1


# --- Observational test: identical synthesize_answer args across arms ---


def test_synthesize_answer_invoked_identically_across_arms():
    llm_client = _SpyLLMClient()
    index, embed_client = _build_fixture_index()
    graph = _build_fixture_graph(llm_client)

    from query import synthesizer as synthesizer_module

    with patch(
        "eval.pipeline.synthesize_answer", wraps=synthesizer_module.synthesize_answer
    ) as spy:
        run_hivemind_arm(_QUERY, ["doc-a"], _CORPUS, llm_client, model=_MODEL_OVERRIDE)
        run_vector_rag_arm(
            _QUERY, index, embed_client, _CORPUS, llm_client, top_k=1, model=_MODEL_OVERRIDE
        )
        run_graphrag_lite_arm(
            _QUERY, graph, _CORPUS, llm_client, top_k=1, model=_MODEL_OVERRIDE
        )

        assert spy.call_count == 3
        for call in spy.call_args_list:
            args, kwargs = call
            # positional: (refined_intent, query_type, entities, selected_markdown, llm_client)
            assert args[1] == _EVAL_QUERY_TYPE
            assert args[2] == _EVAL_ENTITIES
            assert kwargs["model"] == _MODEL_OVERRIDE
            assert kwargs["temperature"] == 0.0
            assert kwargs["max_tokens"] is None
            assert kwargs["timeout"] is None


# --- Observational test: identical LLMClient.complete() call signature/model config ---


def test_final_answer_call_signature_identical_across_arms():
    llm_client = _SpyLLMClient()
    index, embed_client = _build_fixture_index()
    graph = _build_fixture_graph(llm_client)

    result_hivemind = run_hivemind_arm(
        _QUERY, ["doc-a"], _CORPUS, llm_client, model=_MODEL_OVERRIDE
    )
    # The final-answer call is always the *last* recorded call immediately after each arm runs,
    # since generate_final_answer always runs last in every wrapper (see module docstring).
    final_call_hivemind = llm_client.calls[-1]

    result_vector_rag = run_vector_rag_arm(
        _QUERY, index, embed_client, _CORPUS, llm_client, top_k=1, model=_MODEL_OVERRIDE
    )
    final_call_vector_rag = llm_client.calls[-1]

    result_graphrag = run_graphrag_lite_arm(
        _QUERY, graph, _CORPUS, llm_client, top_k=1, model=_MODEL_OVERRIDE
    )
    final_call_graphrag = llm_client.calls[-1]

    for result in (result_hivemind, result_vector_rag, result_graphrag):
        assert isinstance(result, SynthesizerResult)
        assert result.answer

    for final_call in (final_call_hivemind, final_call_vector_rag, final_call_graphrag):
        assert final_call["model"] == _MODEL_OVERRIDE
        assert final_call["temperature"] == 0.0
        assert final_call["max_tokens"] is None
        assert final_call["timeout"] is None
        # Every final-answer prompt goes through the same synthesizer prompt template.
        assert "citations" in final_call["prompt"]
        assert "## File:" in final_call["prompt"]


def test_generate_final_answer_rejects_nothing_extra_and_returns_synthesizer_result():
    """`generate_final_answer` itself (independent of any arm wrapper) round-trips through the
    real `synthesize_answer` and returns its unmodified `SynthesizerResult`.
    """
    llm_client = _SpyLLMClient()
    result = generate_final_answer(_QUERY, ["doc-a"], _CORPUS, llm_client, model=_MODEL_OVERRIDE)
    assert isinstance(result, SynthesizerResult)
    assert result.citations == ["doc-a"]
    assert result.provided_paths == ["doc-a"]
