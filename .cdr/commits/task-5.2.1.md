# task-5.2.1: Tuned classic vector-RAG baseline (issue #27)

## Summary

Issue #27 (milestone #7, Phase 5) requires a genuinely competitive "classic
vector RAG" retrieval arm to compare against HiveMind's own graph-based
retrieval in the upcoming benchmark suite. Per docs/HLD.md's stated risk and
docs/LLD/eval.md, a carelessly weak baseline (arbitrary chunk size, no real
embeddings, a keyword stand-in mislabeled as vector search) would make
HiveMind's own approach look artificially better by comparison. Subtask
5.2.1 ships a fixed-size-chunk vector-RAG baseline that is honestly tuned
via a real grid-search sweep and uses genuine local embeddings, not
constants asserted to look reasonable.

## Features

- **`agents/eval/baselines/vector_rag.py`**: word-boundary-snapped
  sliding-window chunker whose size/overlap defaults are the output of a
  real grid-search sweep (`select_chunk_config`) run against the fixture
  corpus/query set using the actual retrieval path. A local
  `OllamaEmbeddingClient` calls Ollama's `/api/embed` endpoint with
  `nomic-embed-text` (a real, dedicated open-weight embedding model, pulled
  locally -- no new pip dependency, reuses the existing `httpx`
  dependency). `VectorRagIndex`/`retrieve_documents` perform genuine
  cosine-similarity vector search with document-level aggregation,
  API-compatible with `ingestion.rawdoc.RawDocument` /
  `ground_truth.RelevantDoc` shapes for a future combined-benchmark
  subtask.
- **`agents/eval/test_vector_rag_baseline.py`**: pure-unit tests for the
  chunker and recall metric, plus live recall/tuning-sweep tests against
  the real local embedding model, mirroring
  `agents/ingestion/test_e2e_smoke.py`'s skip-if-unreachable convention.
- 100% local (Ollama only) -- no OpenRouter/Gemini, no paid API, no
  `.env`.

## Impact

- 3 new files (`agents/eval/baselines/vector_rag.py`,
  `agents/eval/baselines/__init__.py`,
  `agents/eval/test_vector_rag_baseline.py`); no existing file modified.
- 12/12 new tests pass against real (non-mocked) local embeddings.
- Scope deliberately limited to the fixture corpus/queries the issue's own
  test spec describes; does not yet run against the real synthetic-PDF /
  Bitext / Enron corpora (reserved for a future benchmark-execution
  subtask, per explicit user instruction).

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10010-verification`
- Commit: `573478a3972493c0f4f53ae81cbae4b968d6cac2`
- Zero blocking findings. Verifier independently re-ran
  `select_chunk_config` against the same fixture corpus/queries and
  reproduced the implementer's recall table exactly, including the one
  discriminating candidate; confirmed the Ollama-only guarantee holds
  (hardcoded `localhost:11434`, no env-driven provider, no `.env`, no
  OpenRouter/Gemini references outside disclosure prose); confirmed no new
  pip dependency and that only the 3 claimed files plus CDR run artifacts
  were touched; independently re-ran the target test file (12/12 pass)
  and the broader `eval/` suite (all green).

## Release Notes

- Added `agents/eval/baselines/vector_rag.py` and
  `agents/eval/test_vector_rag_baseline.py`: a tuned classic vector-RAG
  baseline (fixed-size chunker + local `nomic-embed-text` embeddings via
  Ollama + cosine-similarity retrieval) for the milestone #7 benchmark
  suite.
- **Non-blocking finding** (`.cdr/index/regression.jsonl`, subtask 5.2.1,
  type `benchmark_fairness_process_gap`, low severity, open): the grid
  search over 9 (chunk_size, overlap) candidates is genuine and
  reproducible, but 8/9 candidates tied at perfect recall@3 on the small
  fixture corpus, so the shipped defaults (80 words, 0% overlap) were
  effectively selected by the tie-break rule (prefer leanest config)
  rather than by clear empirical superiority. Not itself a defect, but
  nothing currently enforces that a future real-corpus benchmark subtask
  (e.g. 5.3.4) re-run this sweep before drawing HiveMind-vs-baseline
  conclusions from these fixture-tuned defaults.
- **Non-blocking finding** (`.cdr/index/regression.jsonl`, subtask 5.2.1,
  type `unreproducible_environment_claim`, low severity, open): the
  implementer's claimed "386/386 passed, zero regressions" full-suite
  result did not reproduce for the verifier; the shared venv's protobuf
  package has drifted, producing 8 failures + 2 collection errors, all
  attributable to a pre-existing protobuf/grpc gencode-vs-runtime version
  mismatch unrelated to this diff (same class already logged from
  subtask 5.1.3's verification, run 10006-verification). No actual
  regression from this commit -- confirmed via git stash/restore that the
  failure pattern is present regardless of this commit's changes.
- Not pushed (per this agent's role -- never pushes).
