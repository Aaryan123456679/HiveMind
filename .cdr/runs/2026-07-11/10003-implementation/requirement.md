# Requirement — task-5.1.2 (issue #26, milestone #7 "Phase 5", subtask 2 of 3)

## Source
GitHub issue #26 body (Phase 5 "Dataset + ground-truth preparation"), `docs/LLD/eval.md`'s
"Dataset loaders" section (third bullet: "Synthetic policy/manual PDFs, seeded with ~30-50
predefined ground-truth topics and deliberate cross-topic references"), and this session's
user message resolving the previously-BLOCKING OQ-1.

## OQ-1 resolution (now unblocked)
- Content-authoring approach: **LLM-authored content, rendered to PDF via a lightweight
  library** (user's own wording: "e.g. reportlab or weasyprint").
- The LLM MUST be the existing `agents/llm` client, configured to use **local Ollama ONLY**.
  OpenRouter and Gemini must NOT be used for this subtask under any circumstances — recorded
  standing preference in `.cdr/memory/pending.md` line 3: OpenRouter/Gemini are reserved
  strictly for benchmarking/results comparisons, never routine content generation, and no
  `.env`/API keys exist yet for those providers. This constraint must be enforced by the
  script itself (explicit `provider="ollama"`), not merely by relying on an absent
  `LLM_PROVIDER` env var default, since a future unrelated `.env` could set that env var to
  something else.

## OQ-2 context (informs design, not this subtask's scope)
Ground truth for the synthetic corpus (subtask 5.1.3, NOT this subtask) will be
**auto-derived from the generation config + a manual spot-check**. This subtask must
therefore emit a **structured, machine-readable generation manifest** (topic seeds +
per-document primary/cross-topic assignments), not just prose PDFs, so 5.1.3 can consume it
directly without re-parsing PDF text to guess ground truth.

## Acceptance criteria (from issue #26 / LLD, as scoped to 5.1.2)
1. Generate a synthetic corpus of policy/manual-style PDFs.
2. Corpus is seeded with predefined topics (structured manifest, extensible toward the LLD's
   ~30-50 topic target — this subtask ships a smaller demonstrative seed set + a script that
   scales unchanged to more topics).
3. Each generated PDF deliberately contains cross-topic references (i.e. a document whose
   primary topic is A also substantively discusses topic B, C, ... per its assigned
   cross-references).
4. Content is authored by the local Ollama-backed `LLMClient` (via `agents/llm`), rendered to
   a real, parseable PDF file.
5. A test spec verifying: N PDFs generated, each PDF's text contains its seeded primary topic
   and at least one cross-topic reference, and PDFs are valid/parseable.

## Impacted modules (per dispatch + implementer's judgment)
- `data/gen_synthetic_pdfs.py` (new) — generation script/library, colocated with the other
  `data/load_*.py` dataset tooling (matches repo convention: `data/` is where dataset
  acquisition/generation lives, `agents/eval/` is where the *loader interface* over datasets
  lives per task-5.1.1).
- `data/synthetic_corpus/topics.yaml` (new) — structured topic-seed manifest (fixture/config).
- `data/test_gen_synthetic_pdfs.py` (new) — test spec (mocked-LLM unit tests + optional
  skippable live-Ollama smoke test, mirroring `agents/ingestion/test_segment_live.py`'s
  established convention).
- No changes to `agents/llm/*`, `agents/eval/datasets.py`, or any existing loader.

## Explicit non-goals
- Ground-truth label file itself (5.1.3's scope).
- Reaching the full ~30-50 topic count in committed fixture data (script supports it; the
  committed manifest seeds a smaller demonstrative set to keep local live-Ollama runs fast).
- Any OpenRouter/Gemini code path or `.env` file.
