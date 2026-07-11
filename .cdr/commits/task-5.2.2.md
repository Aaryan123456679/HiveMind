# task-5.2.2: LLM-based reranking stage for the vector-RAG baseline (issue #27)

## Summary

**Problem:** Subtask 5.2.1 shipped a genuinely tuned classic vector-RAG
baseline, but per docs/HLD.md's stated risk and docs/LLD/eval.md, a
"reranking if time allows" stage was called out as a further step needed
before that baseline can be considered a fair, well-tuned comparison arm
against HiveMind's own graph-based retrieval in the milestone #7 benchmark
suite. The issue's acceptance criterion asks for a toggleable reranking
step that measurably improves precision@k on the fixture set, without
prescribing a specific mechanism ("e.g. cross-encoder", not mandated).

**Solution:** `agents/eval/baselines/vector_rag_rerank.py` reuses 5.2.1's
`vector_rag.py` (chunker, `OllamaEmbeddingClient`, `VectorRagIndex`,
`retrieve_documents`) unmodified for first-stage retrieval, and adds a
second-stage reranker: a listwise prompt sent to the already-running
local Ollama LLM via `agents/llm/client.py`'s existing `LLMClient`
interface (`llama3.1:8b`), rather than a new dedicated cross-encoder
model. Since `agents/pyproject.toml` has no torch/sentence-transformers
today, adding one purely for a "time-permitting" step would cut against
5.2.1's own no-new-library precedent and the repo's standing Ollama-only
preference; this tradeoff is disclosed in full in the subtask's
architecture-discovery.md. Because precision@k/recall@k are
set-membership metrics (reordering members already inside a fixed top-k
changes neither), the design always retrieves a candidate pool larger
than `top_k` via vector search, then reranks and truncates -- so
reranking can actually change which documents land in the final top-k,
not just their order.

## Features

- **`agents/eval/baselines/vector_rag_rerank.py`**: toggleable
  LLM-listwise reranking stage on top of 5.2.1's vector-RAG baseline,
  using a wider first-stage candidate pool (`DEFAULT_EXTRA_CANDIDATES` /
  `candidate_pool_size`) so reranking can genuinely change top-k
  membership, not just intra-top-k order.
- **`agents/eval/test_vector_rag_rerank.py`**: pure-unit tests
  (precision@k, response-parsing edge cases) plus live-local tests
  (skip-if-Ollama-unreachable) exercising the full retrieve-then-rerank
  path end to end against the real local model.
- 100% local (Ollama only) -- no OpenRouter/Gemini, no paid API, no
  `.env`, no new pip dependency.

## Impact

- 2 new files (`agents/eval/baselines/vector_rag_rerank.py`,
  `agents/eval/test_vector_rag_rerank.py`); no existing file modified,
  including `vector_rag.py` itself (imported read-only).
- 18/18 new tests pass against real (non-mocked) local Ollama
  (`nomic-embed-text` + `llama3.1:8b`); 62/62 across the full `eval/`
  suite, no regressions.
- Real measured result on the fixture set: precision@1 without reranking
  = 0.0 (an off-topic distractor wins), with reranking = 1.0 (the true
  answer is promoted to top-1) -- a genuine, reproduced improvement, not
  a fabricated one.
- Scope deliberately limited to the small fixture corpus/query set (per
  5.2.1's own precedent); candidate-pool-size constants are tuned for
  that fixture scale only (see non-blocking finding below) and will need
  re-tuning before a real-corpus benchmark run.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10013-verification`
- Commit: `3b8e8d3d899a431362ff322e086acd5723093180`
- Zero blocking findings. Verifier independently re-ran
  `eval/test_vector_rag_rerank.py -v` (18 passed, 0 skipped), the broader
  `eval/` suite (62 passed), and
  `TestLiveLocalReranking::test_reranking_improves_precision_at_k_over_baseline`
  in isolation across 8 repeats (8/8 passed), confirming the reported
  precision@1 improvement is reproducible and not flaky; ran `ruff check`
  on the new files (clean); confirmed only the 2 claimed files were
  touched and `vector_rag.py` was imported read-only, not modified. Full
  `agents/` suite showed 2 pre-existing collection errors from a
  protobuf version mismatch, unrelated to this diff and already tracked
  from prior subtasks' verifications.

## Release Notes

- Added `agents/eval/baselines/vector_rag_rerank.py` and
  `agents/eval/test_vector_rag_rerank.py`: a toggleable LLM-listwise
  reranking stage (via the existing local `llama3.1:8b` Ollama model,
  no new dependency) on top of the 5.2.1 classic vector-RAG baseline,
  giving a genuine, reproducible precision@1 improvement (0.0 -> 1.0) on
  the fixture set for the milestone #7 benchmark suite.
- **Non-blocking finding** (`.cdr/index/regression.jsonl`, id
  `hivemind-issue27-5.2.2-fixture-pool-size-scale-limitation`, type
  `performance_scope_limitation`, low severity, open):
  `DEFAULT_EXTRA_CANDIDATES=2` / `candidate_pool_size=3` are fixture-scale
  constants (disclosed in the module docstring); a future full-synthetic-
  corpus benchmark run must re-tune the candidate pool size empirically
  rather than reusing the fixture default unmodified.
- Not pushed (per this agent's role -- never pushes).
