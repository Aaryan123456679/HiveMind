# Architecture Discovery

Token order followed: index/ -> memory/ + handoffs -> targeted LLD -> touched files -> source.

- `.cdr/index/regression.jsonl` line 151 (`hivemind-issue55-4.5.17.3-negative-score-guard-verified`,
  run `260-verification`) already documents, in its `recommendation` field, that the literal
  issue #55 test-spec collects zero tests and that the actual tests live in
  `test_topic_selector_expansion.py`. This is the authoritative prior record confirming the
  defect this subtask must correct a pointer to.
- `.cdr/memory/pending.md` line 83 references the consolidation of this finding into issue #57
  subtask 4.5.18.5 but does not itself contain a standalone, surgical "Resolution note" with the
  corrected command — this subtask adds that.
- Confirmed via `git show --stat 656e35a` that commit `656e35ac09bc94cd3e41dc602c2210b229c4b5f2`
  (issue #55, subtask 4.5.17.3) touched `agents/query/topic_selector.py` and
  `agents/query/test_topic_selector_expansion.py` (plus other pre-existing topic_selector test
  files, unchanged in content per the stat).
- Grepped `agents/query/test_topic_selector_expansion.py` and found the negative-score guard
  tests under the "is_insufficient_alone negative/zero top_score hardening" section (lines
  ~170-193): `test_is_insufficient_alone_negative_score_top_topic_never_flagged` and
  `test_is_insufficient_alone_negative_score_never_flags_lower_score_topic`.
- Ran `cd agents && python -m pytest query/test_topic_selector_expansion.py -k negative_score -v`
  (repo's pytest rootdir is `agents/` per `agents/pyproject.toml`, not repo root — running from
  repo root fails with `ModuleNotFoundError: No module named 'llm'` since `agents/` must be on
  the Python path). Confirmed 2 tests collected and passed, 15 deselected.
- No source-code change is required for this subtask; impacted module is `.cdr/memory/pending.md`
  only, per the issue body itself.
