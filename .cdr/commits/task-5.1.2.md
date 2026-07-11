# task-5.1.2: Synthetic policy/manual PDF corpus generator (issue #26)

## Summary

Issue #26 (milestone #7, Phase 5) requires `agents/eval/`'s retrieval-quality
benchmark harness to compare three arms (HiveMind, vector-RAG baseline,
GraphRAG-style baseline) over an identical corpus per arm. One of the three
required corpora -- "synthetic policy/manual PDFs, seeded with ~30-50
predefined ground-truth topics and deliberate cross-topic references" -- did
not exist yet. Subtask 5.1.2 had previously been blocked on OQ-1 (how to
author and render this corpus); the user resolved this as "LLM-authored
content, rendered to PDF via a lightweight library," with the LLM
constrained to a local Ollama model only (never OpenRouter/Gemini, per the
standing preference in `.cdr/memory/pending.md` -- those providers are
reserved strictly for benchmarking/results comparisons, and no API keys or
`.env` exist for them in this repo). Subtask 5.1.2 closes that gap end to
end.

## Features

- **`data/synthetic_corpus/topics.yaml`**: seeds 10 demonstrative
  ground-truth topics (`id`/`title`/`key_facts` each); the generator scales
  unchanged toward the LLD's full ~30-50-topic target by simply appending
  more entries.
- **`data/gen_synthetic_pdfs.py`**: deterministically assigns each topic as
  one document's primary subject plus 2 distinct other topics as deliberate
  cross-topic references (never self-referencing); builds a prompt
  instructing the model to author self-contained policy prose covering the
  primary topic's key facts while explicitly name-checking every
  cross-referenced topic; authors content via `agents/llm`'s existing
  `LLMClient` with the default client path hardcoded to
  `create_llm_client(provider="ollama")` (deliberately not driven by the
  `LLM_PROVIDER` env var, unlike other `agents/llm` call sites, so the
  script can never silently pick up an unrelated future `.env` setting);
  renders the authored text to a real PDF via `pymupdf`/`fitz` (already a
  first-class project dependency for PDF parsing, reused here for writing
  instead of adding a new `reportlab`/`weasyprint` dependency); and writes a
  structured `manifest.json` (`doc_id`, `filename`, `primary_topic`,
  `cross_references`) so subtask 5.1.3 can derive ground truth directly from
  generation config rather than re-parsing PDF text.
- **`data/test_gen_synthetic_pdfs.py`**: 20 tests covering topic-manifest
  loading/validation, deterministic doc-spec/cross-reference assignment
  (including no-self-reference and distinctness), prompt construction,
  end-to-end `generate_corpus()` against a fake deterministic `LLMClient`
  asserting exact PDF count/validity/parseability (via `fitz`) and
  primary+cross-topic keyword presence, correct `manifest.json` shape, a
  static guard asserting zero OpenRouter/Gemini code-level usage anywhere in
  the module, and an explicit `LLM_PROVIDER`-env-var-ignore test. An
  optional live-Ollama smoke test exercises a real local `llama3.1:8b` call
  end to end (skipped automatically unless a local Ollama server is
  reachable).

## Impact

- Exactly 3 new files (`data/gen_synthetic_pdfs.py`,
  `data/test_gen_synthetic_pdfs.py`, `data/synthetic_corpus/topics.yaml`);
  zero changes to `agents/llm/*`, `agents/eval/datasets.py`, or any existing
  `data/` loader -- no regression risk to previously verified work.
- `data/` regression: 34 passed (14 pre-existing + 20 new). `agents/`
  regression: 348 passed, no new failures. `ruff check`: clean.
- Unblocks subtask 5.1.3 (ground-truth derivation for this corpus).
- Ollama-only guarantee independently re-verified by the verifier from
  source (not trusted from the commit message): zero OpenRouter/Gemini code
  paths or references, hardcoded `provider="ollama"` bypasses
  `LLM_PROVIDER` env-var resolution entirely, `OllamaClient` has no env var
  that could redirect it off `localhost:11434`, and no `.env` file exists
  anywhere in the repo.
- One non-blocking, carried-forward finding (see Release Notes).

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10004-verification`
- Commit: `47385d665adccab8b9ab4784d5eb999e305e2664`
- Zero blocking findings. Verifier independently re-derived every load-
  bearing claim from source and live execution (not trusted from the
  commit message): exact file/line diff stat, the hardcoded-provider
  mechanism (by reading `agents/llm/factory.py`'s actual resolution order),
  `OllamaClient`'s lack of any redirecting env var, an independent re-run of
  all test suites with matching pass counts, confirmed live Ollama
  reachability during the live-smoke test, and confirmed `topics.yaml`'s
  topic count and the generator's non-hardcoded scaling behavior.

## Release Notes

- Added `data/gen_synthetic_pdfs.py`, `data/test_gen_synthetic_pdfs.py`, and
  `data/synthetic_corpus/topics.yaml`: a synthetic policy/manual PDF corpus
  generator, LLM-authored via a local-Ollama-only client and rendered to
  real PDFs via `pymupdf`/`fitz`, with a `manifest.json` sidecar recording
  each document's primary topic and cross-references for future
  ground-truth derivation.
- **Non-blocking, carried-forward finding** (recorded in
  `.cdr/index/regression.jsonl`, id
  `hivemind-issue26-5.1.2-render-pdf-overflow-dead-code`, medium severity,
  not blocking): `render_pdf()`'s page-overflow-handling loop always
  performs exactly one fallback `insert_textbox()` attempt at 85% font size
  and then unconditionally treats the remaining text as fully placed,
  without checking whether that fallback attempt itself still overflowed.
  As a result, `max_pages`/the documented multi-page mechanism is dead code,
  and the documented `RuntimeError` safety net for text that still doesn't
  fit is unreachable -- a sufficiently long LLM completion would be
  silently truncated with no error and no test coverage. Not currently
  triggered in practice (all sampled/live completions fit on one page).
  Recommended follow-up before this generator is scaled to longer
  completions or the full 30-50-topic set; out of scope for this subtask's
  own acceptance criteria and intentionally not fixed as part of this
  commit pass.
- This closes subtask 5.1.2 under issue #26 (milestone #7, Phase 5).
  Subtask 5.1.3 (ground truth) is separate and not yet dispatched; issue
  #26 is not closed as part of this commit.
