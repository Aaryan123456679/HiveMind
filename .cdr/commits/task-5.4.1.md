# Task 5.4.1 (issue #29): Explicit check -- does graph-traversal expansion ever hurt precision vs. no-expansion

## Summary

Adds an explicit ablation check to the milestone #7 benchmark suite that directly answers the
question `docs/HLD.md`'s "graph traversal context blow-up" known risk raises: does GraphRAG-lite's
hop-expansion step ever *hurt* precision, not just help recall. Compares the real-hop-count arm
against a forced `max_hops=0` ("no-expansion") arm on the same query/corpus, scoring both through
the existing metrics path so no new precision arithmetic is introduced.

## Features

- `agents/eval/traversal_precision.py`: `compare_traversal_precision()` runs GraphRAG-lite's
  `retrieve_documents()` twice per query -- once at the real configured hop count, once forced to
  `max_hops=0` -- and scores both arms via the existing `eval.metrics.score_arm`, exposing
  `expansion_ever_hurt_precision` as an explicit boolean flag. `CorpusGrowthCheckpoint` /
  `compare_precision_across_checkpoints` / `checkpoints_with_precision_decrease` generalize the same
  comparison across an arbitrary list of corpus-growth checkpoints, deliberately modeled generically
  so real 20%/50%/100% corpus checkpoints (issue #28, subtask 5.3.4) can be wired in later without
  changing this module's comparison logic.
- `agents/eval/test_traversal_precision_check.py`: covers the issue's test spec (a fixture scenario
  where expansion adds a low-relevance neighbor, asserting the comparison correctly flags a
  precision decrease) plus a no-decrease control case and checkpoint-filtering behavior, fully
  offline/deterministic via an in-file stub LLM client.
- Zero forked precision math: both arms are scored via `eval/metrics.py`'s existing `score_arm`
  (from subtask 5.3.1); zero new retrieval logic (reuses `eval/baselines/graphrag_lite.py`'s
  `EntityGraph`/`retrieve_documents`/`DEFAULT_MAX_HOPS` from subtask 5.2.3).
- Zero live API calls: module only forwards `llm_client`/`model` through to
  `graphrag_lite.retrieve_documents`, which only ever calls `llm_client.complete` through the
  provider-agnostic interface; no network access, environment variable, or `.env` read happens in
  this module.

## Impact

Closes the explicit "expansion can hurt precision" gap named in `docs/HLD.md`'s known-risks list
and restated in `docs/LLD/eval.md`'s known-risks section. Third subtask under milestone #7's
benchmark suite to land this cycle (following 5.3.1/5.3.2/5.3.3 under issue #28); issue #29 tracks
this check independently of issue #28's corpus-growth-checkpoint wiring (subtask 5.3.4, still open).
Wiring real corpus-growth checkpoints into `compare_precision_across_checkpoints` remains explicit
future work for subtask 5.3.4, tracked in `.cdr/memory/pending.md` along with a shipped test-coverage
gap that subtask should close or independently confirm before trusting this function's output.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run:** `.cdr/runs/2026-07-12/005-verification` (`verification.json`, `metadata.json`, `handoff.json`)
- Verifier independently reconstructed issue #29's text via `gh issue view 29`, re-derived the core
  comparison logic from source (not from the implementer's reasoning/plan), and built three original
  fixtures distinct from the implementer's own doc-a/doc-b scenario: a star/hub-topology
  "expansion hurts" case, a shared-neighbor "expansion neutral" case, and a genuine single-call
  2-checkpoint aggregation case.
- Confirmed the hop-count comparison is genuinely real: two distinct
  `graphrag_lite.retrieve_documents` calls at different `max_hops`, correctly distinguishing both
  the "expansion hurts precision" and "expansion doesn't hurt" cases on the verifier's own fixtures.
- Confirmed `score_arm`/`metrics.py` reuse is genuine via grep: zero forked precision arithmetic.
- Confirmed `compare_precision_across_checkpoints`/`checkpoints_with_precision_decrease` correctly
  aggregate across multiple checkpoints in a single call (verifier's own independently constructed
  2-checkpoint single-call test), correctly ordered and correctly flagged.
- Edge cases (empty query list, zero-delta identical retrieval, entity-less corpus) all handled
  gracefully with no crash or false positive.
- Zero live API calls confirmed (no `OllamaClient`/`httpx`/`OpenRouterClient`/`GeminiClient` imports
  outside docstring prose). No new dependency (`agents/pyproject.toml` diff empty). Zero regression
  risk (diff touches only the 2 new files). Full suite re-run: 149 passed, ruff clean.
- **Non-blocking finding:** the implementer's own shipped test
  (`test_checkpoints_with_precision_decrease_filters_correctly`) never actually calls
  `compare_precision_across_checkpoints()` with a >1-checkpoint list in one invocation -- it calls
  the function twice with singleton lists and manually concatenates the results before filtering.
  The underlying function itself is confirmed correct (verifier's independent multi-checkpoint
  single-call test), but this is a real shipped test-coverage gap. See `.cdr/memory/pending.md`.

## Release Notes

Added an explicit graph-traversal expansion-vs-precision ablation check to the milestone #7
benchmark suite (issue #29, subtask 5.4.1): the benchmark now directly compares GraphRAG-lite's
precision with hop-expansion enabled against a forced no-expansion baseline, and flags any query or
corpus-growth checkpoint where expansion strictly decreases precision. No live provider calls; fully
offline/deterministic. Real corpus-growth-checkpoint wiring remains for subtask 5.3.4 (issue #28).
