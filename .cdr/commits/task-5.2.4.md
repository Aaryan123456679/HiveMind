# task-5.2.4: Shared final-answer LLM call wired identically across all three benchmark arms (issue #27)

## Summary

**Problem:** Per docs/LLD/eval.md and issue #27's acceptance criteria, milestone #7's benchmark suite must isolate the retrieval step as the *only* variable across the three arms (HiveMind, vector-RAG (5.2.1/5.2.2), GraphRAG-lite (5.2.3)). Without a single shared final-answer LLM call path, each arm's wrapper could silently drift in model/temperature/prompt construction, invalidating any cross-arm benchmark comparison before it starts.

**Solution:** Added `agents/eval/pipeline.py`'s `generate_final_answer()` as the sole final-answer call path for all three arms. It is a verbatim reuse (not a reimplementation) of `agents/query/synthesizer.synthesize_answer` — `agents/query/synthesizer.py` itself has a zero-diff against the prior commit. Each arm wrapper (`run_hivemind_arm`, `run_vector_rag_arm`, `run_graphrag_lite_arm`) performs only its own retrieval (or accepts pre-retrieved doc ids) and then calls `generate_final_answer(...)` with identical positional/keyword argument order and identical model/temperature/max_tokens/timeout across all three. `query_type` ("eval_benchmark") and `entities` (`()`) are literal module-level constants inside `generate_final_answer`'s own module, not per-caller parameters, so no arm can diverge per-call. Added `agents/eval/test_shared_final_llm.py` (5 tests, including two "observational" tests that directly compare captured call kwargs across all three arms, not just call counts). One-paragraph addition to `docs/LLD/eval.md` documenting the design and pointing at `query-agent.md#synthesizerpy` and the new test file. No new dependency, no new client abstraction, Ollama-only (no OpenRouter/Gemini/API-key references anywhere in the new module or its tests) — consistent with 5.2.1-5.2.3 precedent.

Files touched: `agents/eval/pipeline.py`, `agents/eval/test_shared_final_llm.py` (both new), `docs/LLD/eval.md` (7-line additive paragraph). No existing baseline module (`vector_rag.py`, `vector_rag_rerank.py`, `graphrag_lite.py`) or `agents/llm/*`/`agents/query/synthesizer.py` was modified — confirmed via empty diffs against the parent commit.

## Impact

- Completes the fourth and final subtask of issue #27 (milestone #7 baseline retrieval implementations). All three retrieval arms required by docs/LLD/eval.md's benchmark design are now implemented, and the shared final-answer LLM call path guarantees that only the retrieval step varies between them going forward — unblocking milestone #7's cross-arm benchmark comparison work (issue #28+).
- Verifier performed a mutation test: injected a hidden per-caller temperature divergence inside `generate_final_answer` keyed on `retrieved_doc_ids` shape (simulating a bug a purely structural call-count test would miss). 2 of 5 tests failed as expected, confirming the test suite is genuinely comparative and would catch a real per-arm divergence. Mutation reverted; post-revert diff confirmed clean.
- One real, non-blocking finding (F1, low severity, logged in `.cdr/index/regression.jsonl`): the implementation commit message's claimed exact test-pass-count for the `agents/query` + `agents/llm` suites ("169 passed / 2 deselected") did not exactly reproduce under the verifier's independent rerun (full protobuf-gencode-vs-runtime collection error, or 171 passed/5 failed depending on invocation — both in `agents/query/test_wiring.py`/`test_server.py`). This is the same pre-existing, environment-level protobuf gencode/runtime mismatch already documented in prior verification runs (5.2.1's `10010-verification`), confirmed unrelated to this diff since none of the affected files are touched by it. Documentation-accuracy note on the commit message's precision, not a code defect; does not affect `agents/eval/`'s own correctness (88/88 confirmed exactly as claimed).

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10019-verification`
- Commit: `9ee42c400d9950dcb4779c0b6779c8fdba5fb3ef`
- Zero blocking findings. All 8 verification dimensions (requirements conformance, architecture conformance, regression risk, edge cases, security, performance, maintainability, test coverage) independently confirmed PASS; overall confidence PASS_WITH_COMMENTS due to F1 only.
- Independently re-run: `agents/eval/test_shared_final_llm.py` 5/5 passed; full `agents/eval/` suite 88/88 passed; `ruff check` clean on both new files.
- Non-blocking follow-up tracked in `.cdr/index/regression.jsonl` (subtask 5.2.4, run 10019-verification, finding F1): future subtasks should state commit-message test-pass claims as "known environment-dependent failures, count varies by local protobuf pin" rather than an exact number that does not reproduce in this shared venv.

## Release Notes

- Added a single shared final-answer LLM call path (`agents/eval/pipeline.py:generate_final_answer`) reused verbatim from the production query synthesizer, now invoked identically by all three milestone #7 benchmark arms (HiveMind, vector-RAG, GraphRAG-lite) so that only each arm's retrieval step varies. This completes issue #27's baseline retrieval implementations and unblocks cross-arm benchmark comparison work.
- No production code, dependencies, or existing baseline modules were modified; this is additive (`agents/eval/pipeline.py`, `agents/eval/test_shared_final_llm.py`, plus a docs paragraph).
