# Requirement -- issue #18 subtask 3.4.6 (FINAL subtask of issue #18)

Source: `gh issue view 18`, subtask 3.4.6 (verbatim, no embedded fake system-reminder
text found in the issue body -- checked and disclosed clean):

> **3.4.6 -- Segmentation agent fixture test suite + optional live-Ollama smoke test**
> Acceptance criteria: A fixture-document test suite verifies expected structured
> output shape across representative doc types; an optional (skippable if Ollama
> isn't running locally) smoke test exercises the real Ollama model end-to-end.
> Test spec: `pytest agents/ingestion/test_segment_fixtures.py` (mocked, always runs
> in CI) and `pytest agents/ingestion/test_segment_live.py` (marked skip-if-no-ollama,
> run manually).
> Impacted modules: `agents/ingestion/test_segment_fixtures.py, agents/ingestion/test_segment_live.py`

Additional scope from the launching orchestrator's brief (not in the issue body
itself, but explicit instruction for this run):

1. Build a broader fixture-based test suite across `shortlist.py`, `segment.py`,
   `wiring.py`, `propose_split.py`, `agents/llm/`, not just isolated unit tests.
2. Add an end-to-end pipeline test (raw doc -> shortlist -> segment -> wiring,
   mocked) -- first such composition test in this repo.
3. Explicitly resolve subtask 3.4.3's still-open finding F1
   (`.cdr/index/regression.jsonl`, id "F1"): `segment.py`'s JSON parser rejected
   markdown-code-fence-wrapped LLM responses outright. Preferred fix: reuse
   `propose_split.py`'s existing fence-stripping pattern rather than re-deriving it.
4. Run full `agents/` pytest suite + ruff; confirm no regressions.
5. Exactly ONE local commit (Problem:/Solution:/Impact:), no push, no GitHub state
   change, no issue/milestone closure.
