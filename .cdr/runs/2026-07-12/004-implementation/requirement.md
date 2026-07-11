# Requirement — Subtask 5.4.1 (issue #29, Phase 5.4)

Source: `gh issue view 29` (live, read directly — not assumed from prior session memory).

Issue #29 title: "[5] Benchmark fairness + traversal-precision checks (agents/eval/)", part of
Epic Phase 5: Benchmark suite. Only one subtask is in scope for this implementation pass:

## Subtask 5.4.1 — Explicit check: does graph-traversal expansion ever hurt precision vs. no-expansion

- **Acceptance criteria**: The benchmark explicitly compares HiveMind's precision with vs.
  without graph-traversal expansion at each corpus-growth checkpoint, and reports any checkpoint
  where expansion decreases precision.
- **Test spec**: `pytest agents/eval/test_traversal_precision_check.py`: fixture scenario where
  expansion adds a low-relevance neighbor, assert the comparison correctly flags a precision
  decrease.
- **Impacted modules**: `agents/eval/traversal_precision.py`,
  `agents/eval/test_traversal_precision_check.py`

## Scoping notes (confirmed against live issue text)

- The issue's phrasing "at each corpus-growth checkpoint" is the acceptance-criteria framing for
  the eventual full benchmark run; the actual wiring of real corpus-growth checkpoints (20%/50%/
  100% ingested) against the real corpus is subtask 5.3.4's explicitly-gated "real benchmark
  execution" scope (out of scope here, per this project's standing convention that only 5.3.2 and
  5.3.4 are authorized to use live paid APIs — this subtask must remain code-only/offline). This
  subtask's own test spec asks only for a **fixture-scenario** correctness check of the
  comparison logic itself, so the module is built generically over an arbitrary
  `EntityGraph`/checkpoint label rather than hard-wiring the real corpus, mirroring 5.2.3's own
  disclosed "fixture-only, corpus-wiring is future work" scope boundary.
- This is Ollama-only (no OpenRouter/Gemini): the only LLM calls this module makes are indirectly
  through `graphrag_lite.retrieve_documents`'s existing entity-extraction path, which the test
  suite exercises exclusively via a deterministic stub `LLMClient` (no live Ollama, no network),
  per the "code-only/offline-testable" scoping constraint for this implementation pass.
- Reuse constraint (per this project's established "canonical home" convention, see
  `metrics.py`'s own docstring): do not reimplement `recall_at_k`/`precision_at_k`/
  `relevant_doc_id_set`/`QueryScore`/`score_query`/`ArmScore`/`score_arm` — import from
  `eval.metrics`. Do not reimplement entity-graph construction/hop-decay traversal — import
  `EntityGraph`, `retrieve_documents`, `DEFAULT_MAX_HOPS` from `eval.baselines.graphrag_lite`. The
  "no-expansion" arm is exactly `retrieve_documents(..., max_hops=0)` (already supported: that
  module's own docstring states `max_hops=0` disables hop expansion entirely, direct-match only).
