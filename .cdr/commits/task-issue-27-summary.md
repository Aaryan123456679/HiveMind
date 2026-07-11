# Issue #27 — Baseline retrieval implementations (Phase 5, milestone #7)

## Summary

Issue #27 (milestone #7, "Phase 5: Benchmark suite") tracked four subtasks
building the three retrieval baselines and the shared final-answer LLM call
path required for milestone #7's cross-arm benchmark comparison. All four
are now implemented and independently CDR-verified **PASS_WITH_COMMENTS**:

- **5.2.1** — Classic vector-RAG baseline: fixed-size chunker (real,
  non-strawman chunk size/overlap tuning) + Ollama `nomic-embed-text`
  embeddings + cosine-similarity retrieval (`agents/eval/baselines/vector_rag.py`).
  Commit `573478a3972493c0f4f53ae81cbae4b968d6cac2`, verified in
  `.cdr/runs/2026-07-11/10010-verification`.
- **5.2.2** — Optional LLM-based listwise reranking stage on top of the
  5.2.1 baseline, local `llama3.1:8b` via the existing `LLMClient`
  (`agents/eval/baselines/vector_rag_rerank.py`); measured real
  precision@1 improvement 0.0 → 1.0 on the fixture set. Commit
  `3b8e8d3d899a431362ff322e086acd5723093180`, verified in
  `.cdr/runs/2026-07-11/10013-verification`.
- **5.2.3** — Simplified GraphRAG-style entity-graph retrieval baseline:
  real LLM entity extraction, stdlib-only co-occurrence `EntityGraph` (no
  `networkx`), capped-hop decayed BFS traversal as the disclosed mitigation
  for the "graph traversal context blow-up" risk
  (`agents/eval/baselines/graphrag_lite.py`). Commit
  `2697d0746583dea2d5a0892c610bd3e0c6d42c41`, verified in
  `.cdr/runs/2026-07-11/10016-verification`.
- **5.2.4** — Shared final-answer LLM call wired identically across all
  three arms: `generate_final_answer()` in `agents/eval/pipeline.py` is a
  verbatim reuse (not a reimplementation) of
  `agents/query/synthesizer.synthesize_answer`, mutation-tested to confirm
  the identical-call-path guarantee across arms is genuinely enforced, not
  merely structural. Commit `9ee42c400d9950dcb4779c0b6779c8fdba5fb3ef`,
  verified in `.cdr/runs/2026-07-11/10019-verification`.

Together these close out **issue #27**, part of **milestone #7 "Phase 5:
Benchmark suite"**. All three retrieval arms (HiveMind, vector-RAG,
GraphRAG-lite) required by `docs/LLD/eval.md`'s three-arm benchmark design
are now implemented, and the shared final-answer call path guarantees only
the retrieval step varies between arms — unblocking milestone #7's
cross-arm benchmark comparison work (issue #28+).

## Features

- **Vector-RAG baseline** (5.2.1) — fixed-size chunking + embedding-based
  cosine-similarity retrieval, no new heavyweight dependency.
- **Vector-RAG reranking** (5.2.2) — toggleable LLM-listwise reranking
  stage, real measured precision@k improvement.
- **GraphRAG-lite baseline** (5.2.3) — entity-graph retrieval with capped-
  hop decayed BFS traversal, output shape matching `vector_rag.py` exactly.
- **Shared final-answer LLM path** (5.2.4) — single `generate_final_answer`
  function reused by all three arms, eliminating 3x duplication risk and
  guaranteeing benchmark fairness on the final-answer-generation step.

## Impact

- Completes milestone #7's baseline-implementation phase in full: all three
  retrieval arms plus the shared final-answer call path are implemented,
  independently verified, and committed.
- Two non-blocking findings remain open in `.cdr/index/regression.jsonl`,
  neither blocking this issue's closure:
  - 5.2.3: unguarded substring-containment fallback in
    `_match_query_entities` (medium severity, benchmark-fairness risk at
    real-corpus scale, not a defect at fixture scale).
  - 5.2.4: commit message's claimed exact test-pass-count for
    `agents/query`/`agents/llm` did not exactly reproduce under the
    verifier's independent rerun — confirmed to be the same pre-existing,
    environment-level protobuf gencode/runtime mismatch already documented
    in 5.2.1's own verification (`10010-verification`), unrelated to any of
    the four subtasks' actual diffs (none touch the affected files).
- All four subtasks' commits (`573478a`, `3b8e8d3`, `2697d07`, `9ee42c4`)
  already follow this repo's Problem/Solution/Impact commit-message
  standard — no deviation to note, no git history rewrite needed.
- Consistent with prior issue-close precedent (e.g. issue #26): all four
  commits are local-only (not pushed); issue #27 is not closed on GitHub as
  part of this bookkeeping step's commit — that is handled by the
  orchestrator's separate push/close step, mirrored here via `gh issue
  close 27` with a resolution comment (issue itself closed, no push).

## Verification

| Subtask | Commit | Verdict | Verification run |
|---|---|---|---|
| 5.2.1 | `573478a` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/10010-verification` |
| 5.2.2 | `3b8e8d3` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/10013-verification` |
| 5.2.3 | `2697d07` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/10016-verification` |
| 5.2.4 | `9ee42c4` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/10019-verification` |

## Release Notes

- Added the full milestone #7 baseline retrieval suite: a tuned classic
  vector-RAG baseline with optional LLM reranking, a GraphRAG-lite entity-
  graph baseline, and a shared final-answer LLM call path reused verbatim
  from the production query synthesizer and invoked identically by all
  three arms.
- This closes issue #27 under milestone #7 "Phase 5: Benchmark suite".
  Non-blocking follow-ups tracked in `.cdr/index/regression.jsonl`:
  harden the GraphRAG-lite entity-match fallback's minimum-length guard
  before benchmarking against the full real corpus (5.2.3); pin
  protobuf/grpcio versions to stop shared-venv drift from silently
  invalidating full-suite regression claims across subtasks (5.2.1, 5.2.4,
  and prior).
