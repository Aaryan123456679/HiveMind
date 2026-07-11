# Requirement — Subtask 4.5.18.5

GitHub issue #57 (repo Aaryan123456679/HiveMind), subtask **4.5.18.5 — (INFO) Fix issue #55's
4.5.17.3 literal test-spec path collecting zero tests** (final remaining subtask of #57).

Background (from issue body): issue #55's 4.5.17.3 subtask's own literal test-spec
(`pytest agents/query/test_topic_selector.py -k negative_score`) collects zero tests — the
negative-score test actually landed in a different file. Silent false-pass trap for anyone
following the issue text literally.

Acceptance criteria: correct the test-spec reference (in this new issue's own record, plus a
note in `.cdr/memory/pending.md`) to the actual file/test name so future verification doesn't
silently no-op.

Test spec (this subtask): confirm `pytest <correct file path> -k negative_score` actually
collects and passes >=1 test.

Impacted modules: `.cdr/memory/pending.md`
