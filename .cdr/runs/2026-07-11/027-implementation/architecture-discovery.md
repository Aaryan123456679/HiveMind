# Architecture Discovery -- 4.5.2

## Index/memory tokens consulted (order: index -> memory/handoffs -> LLD -> touched files -> source)

- `.cdr/index/regression.jsonl` tail: `F-4.5.1-1` (SynthesizerResult.citations dedup not
  code-enforced -- irrelevant here since `unknown_citations()` dedups internally regardless)
  and `F-4.5.1-2` (unknown_citations() exact-match adversarially tested, no false negatives).
  Both PASS_WITH_COMMENTS, non-blocking, from `.cdr/runs/2026-07-11/026-verification`.
- `.cdr/runs/2026-07-11/025-implementation/` and `026-verification/` (4.5.1's own run dirs)
  confirm 4.5.1 committed at `c8c49cf`, verdict PASS_WITH_COMMENTS, not pushed.
- `docs/LLD/query-agent.md`'s "synthesizer.py" section: read for the "final LLM call ->
  answer with inline file-path citations" contract; no further citation-validation detail
  beyond what 4.5.1's module docstring already captured/disclosed.
- Touched-file candidates: `agents/query/synthesizer.py` (read in full, 320 lines) and
  `agents/query/test_synthesizer.py` (read in full, 273 lines) -- per task instructions,
  read directly rather than guessed.

## Key findings

- `SynthesizerResult.unknown_citations()` (synthesizer.py:163-178) is the complete, correct,
  already-tested building block. No source-level gap found (see requirement.md's gap
  analysis) -- `synthesize_answer()`'s deliberate non-raising behavior is 4.5.1's own settled,
  disclosed design, not an omission.
- `test_synthesizer.py` already has two `unknown_citations()`-focused tests
  (`test_unknown_citations_flags_path_not_in_provided_set`,
  `test_unknown_citations_deduplicated_and_order_preserved`) using the exact
  `_FakeLLMClient` + `synthesize_answer()` end-to-end pattern this subtask's test spec
  describes. This subtask's dedicated file must use the same pattern (LLMClient mocked via a
  fake subclass, not `MagicMock(spec=...)`) for consistency with package convention flagged
  in `F-4.4.4-2`.
- `agents/llm/client.py`: `LLMClient` is an ABC with single abstract method `complete(prompt,
  *, model=None, temperature=0.0, max_tokens=None, timeout=None) -> str`; `LLMError` is its
  failure exception, unrelated to citation validation.
- No other module in `agents/query/` calls `synthesize_answer()` yet (no integration/pipeline
  wiring exists downstream of 4.5.1 as of this run) -- confirmed via grep below.

## Conclusion

Additive-only, test-file-only change. No synthesizer.py modification planned (see
requirement.md decision). No other files impacted.
