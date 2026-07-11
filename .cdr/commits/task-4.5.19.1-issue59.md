# task-4.5.19.1 (issue #59): Python-side LLM interceptor for latency/cost recording

## Summary

**Problem:** `agents/eval/cost_latency.py` (subtask 5.3.3, issue #28) defines the `StageRecord` schema and the `aggregate_by_stage`/`rollup_cost_per_1000_queries` rollup functions a real per-call interceptor would need to feed, but nothing in `agents/llm/` populated that schema from actual `LLMClient` calls. `resolve_cost_usd()` would raise `ValueError` on the first non-Ollama `StageRecord` in any future benchmark run (5.3.4), since no client surfaced token usage and no pricing table existed anywhere in the repo -- a forward-dependency gap explicitly disclosed and tracked from 5.3.3's own verification.

**Solution:** `agents/llm/interceptor.py` adds `LLMInterceptor`, a thin wrapper around `LLMClient` calls that measures `duration_seconds` directly via `time.perf_counter()` and resolves `cost_usd`: always `$0.0` for Ollama, and for OpenRouter/Gemini computed from the provider's real reported token usage times a small, explicitly-labeled-as-illustrative, injectable per-model rate table (`DEFAULT_RATE_TABLE`, override via `rate_table=`). Token usage previously discarded by `openrouter_client.py`'s `usage` and `gemini_client.py`'s `usageMetadata` response fields is now surfaced through a new, non-abstract `LLMClient.complete_with_usage()` method (`client.py`) that each client overrides; the existing public `complete()` method's signature, return type, and behavior are unchanged (both call a shared private `_do_complete()` internally to avoid duplicating HTTP logic).

## Features

- `agents/llm/interceptor.py`: `LLMInterceptor` (per-call wrapper emitting `cost_latency.StageRecord`-shaped output), `LLMInterceptorError` (loud failure on uncatalogued model or missing usage data -- no silent defaulting), `DEFAULT_RATE_TABLE` (injectable per-model cost-per-token rates for `openai/gpt-4o-mini` and `gemini-2.5-flash`).
- `agents/llm/client.py`: new `TokenUsage`/`CompletionResult` types and a non-abstract `complete_with_usage()` method on the `LLMClient` interface, with a shared private `_do_complete()` seam so `complete()`'s existing contract is untouched.
- `agents/llm/openrouter_client.py` / `agents/llm/gemini_client.py`: real token-usage parsing (`usage` / `usageMetadata` response fields respectively) wired into `complete_with_usage()`.

## Impact

Closes the forward-dependency gap flagged during 5.3.3's verification (`.cdr/index/regression.jsonl`, commit `50c2b06`): `agents/eval/cost_latency.py`'s `resolve_cost_usd()` no longer faces an unresolvable `ValueError` the moment a real (non-Ollama) benchmark record shows up, because `agents/llm/` now has a real, tested source of per-call cost/latency data feeding that exact schema. This unblocks issue #28 subtask 5.3.4 (real corpus-growth-checkpoint benchmark execution) from a code-readiness standpoint -- the live-API-spend caps and go-ahead gating recorded in `.cdr/memory/pending.md` for 5.3.4 itself remain independently in force and are unaffected by this commit.

Backward compatibility is fully intact: `complete()`'s existing behavior, signature, and return type are unchanged across all three clients (confirmed via empty diff on pre-existing test files and both old and new test suites passing unmodified). No new dependency (`agents/pyproject.toml` diff empty).

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10026-verification`
- Commit verified: `84415ad614b5c142422e15bec45c9716496f2067` (parent `350e1b0071b178e6b5a599a4172045a28f96cd92`)
- Verifier independently confirmed: `complete()` backward compatibility fully intact (empty diff on existing test files, both suites pass unmodified); cost math hand-verified correct for both cataloged models (`openai/gpt-4o-mini`, `gemini-2.5-flash`); error paths (uncatalogued model, missing usage) raise `LLMInterceptorError` loudly rather than silently defaulting; `StageRecord`/`cost_latency.py` interoperability confirmed via the verifier's own independently-constructed `StageRecord`s fed through `aggregate_by_stage()`/`rollup_cost_per_1000_queries()`; full suite re-run confirmed 207 passed; ruff clean; no new dependency; zero live API calls (`test_interceptor.py` uses only `httpx.MockTransport`).
- One low-severity, non-blocking finding: a pre-existing (not introduced by this commit) id collision in `.cdr/index/task.jsonl:150` -- the literal key `"task-4.5.19.1"` was already assigned to an unrelated, already-verified Go subtask from issue #58 (commit `4b9a175`). Confirmed via `git log`/`git diff` that this collision predates this commit. Resolved as part of this bookkeeping commit by giving this Python subtask (issue #59) the disambiguated key `task-4.5.19.1-issue59` in `task.jsonl`, without altering or renumbering the pre-existing issue #58 entry. See `.cdr/memory/pending.md` for the cross-reference note.

## Release Notes

- Added a Python-side LLM call interceptor (`agents/llm/interceptor.py`) that measures per-call duration and resolves cost (free for Ollama, rate-table-based for OpenRouter/Gemini), emitting output directly compatible with `agents/eval/cost_latency.py`'s `StageRecord`/aggregation pipeline (issue #59, milestone #7 / Phase 5 benchmark-suite cost-latency follow-up to 5.3.3, issue #28).
- No behavior change to existing `LLMClient.complete()` callers; purely additive.
