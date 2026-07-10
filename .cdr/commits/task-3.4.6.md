# task-3.4.6: Segmentation fixture suite + live-Ollama smoke test — F1 closure (FINAL subtask of issue #18)

## Summary

Closes out GitHub issue #18 (segmentation-agent epic, milestone #5, "Phase
3"): the sixth and final of 6 subtasks. Prior subtasks (3.4.1-3.4.5) each
unit-tested their own module in isolation with hand-built synthetic
payloads; nothing proved the pipeline's pieces actually compose (`shortlist`
's output shape flowing into `segment`'s input, `segment`'s output into
`wiring`'s input), and nothing exercised a real Ollama model's actual
response format. This subtask adds a realistic multi-topic fixture corpus,
the repo's first end-to-end pipeline-composition test, an optional
live-Ollama smoke test, and closes subtask 3.4.3's long-standing forwarded
finding F1 (markdown-code-fence-wrapped LLM JSON being rejected instead of
parsed) by extracting the fence-stripping logic `propose_split.py` (3.4.5)
had already built for itself into a shared helper and wiring `segment.py` to
use it too.

## Features

- **Shared `agents/ingestion/_json_fences.strip_code_fences` helper**:
  extracted, behavior-preserving, from `propose_split.py`'s private
  fence-stripping regex (confirmed via diff: the extraction changed only the
  import/call site, not the underlying logic). `segment.py`'s
  `_parse_segment_json` now calls the same helper before `json.loads`,
  closing F1 by reuse rather than re-deriving an equivalent regex a second
  time — eliminating the two-copies-of-the-same-logic smell that was the
  root cause of F1 ever existing.
- **Realistic multi-topic fixture corpus**
  (`agents/ingestion/testdata/notes_corpus/`): four small markdown notes
  files spanning billing/invoice-disputes, billing/refund-requests, an
  engineering on-call runbook, and an unrelated HR topic, deliberately
  sharing entities and cross-references across files to give the fixture
  suite something non-trivial to shortlist/segment against.
- **`agents/ingestion/test_segment_fixtures.py`** (always runs in CI, fully
  mocked): structured-output-shape coverage across representative document
  types (notes, email, ticket), plus the repo's first end-to-end composition
  test threading `shortlist()`'s real output into `segment()`'s real input
  into `execute_segment()`'s real input — not three isolated unit tests
  stitched together after the fact.
- **`agents/ingestion/test_segment_live.py`** (optional smoke test): a
  module-wide `pytest.mark.skipif`, automatically skipped unless a real
  local Ollama server is reachable, exercising `segment()`/`propose_split()`
  against a genuine model response rather than a mock. The verifier
  confirmed this was genuinely executed (not silently skipped) against a
  running local Ollama instance during verification.

## Impact

- Scope correctly contained to `agents/ingestion/`: `git diff 656b612..48f1845
  -- engine/ proto/` is empty — no new gRPC/proto surface introduced,
  independently confirmed rather than taken on trust.
- No production code path in `segment.py` changed other than the
  fence-stripping call site; existing tests plus a full-suite rerun show no
  new failures introduced.
- Full `agents/` regression suite: 153 passed, 2 pre-existing failures
  (protobuf gencode/runtime-version mismatch, independently confirmed via
  git-worktree bisection to also fail at parent commit 656b612 — not caused
  by this change). `ruff` clean on `agents/ingestion/` and `agents/llm/`;
  the one pre-existing F401 in generated `agents/hivemind_pb2_grpc.py` is
  confirmed untouched by this diff.
- **F1 (medium, `hivemind-issue18-3.4.3-segment-json-parsing`) is now
  CLOSED.** Independently confirmed via: (1) diff review showing
  `_json_fences.py` is a genuine behavior-preserving extraction of
  `propose_split.py`'s original regex, zero logic change; (2) confirming
  `segment.py` has a single JSON-parsing code path
  (`_parse_segment_json`) and it now calls `strip_code_fences` before
  `json.loads`; (3) the new fixture suite's composition test exercising
  fence-wrapped mocked LLM output end-to-end; (4) the live-Ollama smoke
  test's real run. Marked CLOSED in `.cdr/index/regression.jsonl` and
  `.cdr/memory/pending.md`.
- **One new low-severity, non-blocking finding**
  (`hivemind-issue18-3.4.6-F1-fence-helper-gap`): the shared
  `strip_code_fences` helper only strips a single leading/trailing fence
  wrapping the entire response; it does not handle multiple sequential
  fence blocks or prose-wrapped fences (a fenced block preceded/followed by
  other text). This is not a new regression — the identical limitation
  pre-existed in `propose_split.py` before extraction — and is out of F1's
  literal scope (single-fence-wrap case), so it is non-blocking. Recorded in
  `.cdr/index/regression.jsonl` and `.cdr/memory/pending.md`, forward-
  referenced to GitHub milestone #10 (Phase 4.5 follow-ups).
- **Issue #18 is now fully implemented across all 6 subtasks
  (3.4.1-3.4.6), each independently PASS/PASS_WITH_COMMENTS-verified, and
  committed locally.** See the consolidated closure summary in
  `.cdr/commits/task-issue-18-summary.md` for the full subtask list and the
  still-open, carried-forward findings (F2-F6) that must be surfaced to the
  user before any push/close decision. This commit does not push and does
  not close issue #18 or any GitHub milestone state — that requires
  separate, fresh user authorization.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-10/035-verification`
- All 11 verification dimensions PASS or PASS_WITH_COMMENTS; zero
  must-fix/blocking findings. `f1_closure_claim`,
  `end_to_end_composition_test_claim`, and `live_ollama_smoke_test_claim`
  each independently re-derived by the verifier (diff inspection,
  adversarial fence inputs, grep for prior composition tests, a live pytest
  run against a running Ollama instance) rather than taken on the
  implementer's word. Confidence: high.

## Release Notes

- Added `agents/ingestion/_json_fences.py` (shared `strip_code_fences`
  helper) and wired both `segment.py` and `propose_split.py` to use it,
  closing issue #18 finding F1 (markdown-code-fence-wrapped LLM JSON was
  previously rejected by `segment.py` instead of parsed).
- Added `agents/ingestion/testdata/notes_corpus/` (4-file realistic
  multi-topic fixture corpus), `agents/ingestion/test_segment_fixtures.py`
  (structured-output coverage plus the first end-to-end
  `shortlist -> segment -> execute_segment` composition test), and
  `agents/ingestion/test_segment_live.py` (optional live-Ollama smoke test,
  auto-skipped when no local Ollama server is reachable).
- New non-blocking finding: shared fence-stripping helper does not handle
  multiple sequential fence blocks or prose-wrapped fences
  (`hivemind-issue18-3.4.6-F1-fence-helper-gap`), inherited from
  `propose_split.py`'s pre-existing limitation; non-blocking.
- This is issue #18's final subtask. All 6 subtasks (3.4.1-3.4.6) are now
  implemented, verified, and committed locally. Not pushed; issue/milestone
  not closed — see `.cdr/commits/task-issue-18-summary.md` for the
  consolidated summary and the still-open findings (F2-F6) that should be
  surfaced to the user before any push/close authorization.
