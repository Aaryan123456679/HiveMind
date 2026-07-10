# task-3.4.3: Segmentation prompt + structured JSON output parsing

## Summary

Adds `agents/ingestion/segment.py`'s `segment()` function: the third of 6
subtasks in GitHub issue #18 (segmentation-agent epic, milestone #5 "Phase
3"). Given a document (`RawDocument`, from 3.3.4) and a bounded topic
shortlist (`shortlist()`, from 3.4.2), `segment()` builds a segmentation
prompt, calls it through an `LLMClient` (from 3.4.1), and parses the raw
completion string into a validated `SegmentResult` matching
`docs/LLD/ingestion-agent.md`'s flat JSON output shape. Issue #18 has **3
subtasks remaining** (3.4.4-3.4.6); this is not a closure commit.

## Features

- **`SegmentResult` schema**: frozen dataclass mirroring the LLD's flat JSON
  shape exactly: `topic_action` (`Literal["APPEND_EXISTING", "CREATE_NEW"]`),
  `target_topic`, `new_topic_path`, `content_markdown` (all `str`), and
  `entities`, `related_topics` (both `list[str]`, a disclosed choice since
  neither the issue, the LLD, nor `PutSegmentRequest`'s proto shape specify a
  richer element type).
- **Exception hierarchy design**: a new `SegmentError` base, deliberately
  *not* a subclass of `llm.client.LLMError`. `LLMError` means "the provider
  call itself failed"; `SegmentParseError` (the sole concrete subclass) means
  "the call succeeded but its output is unusable" — distinct failure modes a
  caller needs to tell apart (retry-worthy vs. not). `LLMError` raised by
  `llm_client.complete()` propagates unwrapped through `segment()`, verified
  both by a shipped test and by the verifier's independent ad hoc script.
- **Structured JSON parsing/validation** (`_parse_segment_json`): checks JSON
  validity, top-level-object shape, presence of all 6 required keys,
  per-field type (str for the four scalar fields, `list[str]` for
  `entities`/`related_topics`, including element-type checking), and
  `topic_action` is one of the two LLD literals. Every failure raises
  `SegmentParseError` naming the specific field/reason — no silent partial
  results.
- **Cross-field validation** (disclosed, intentionally narrow scope):
  `APPEND_EXISTING` requires a non-empty `target_topic`; `CREATE_NEW`
  requires a non-empty `new_topic_path`. This is the single cross-field rule
  the issue's own example malformed case describes. `target_topic` is
  deliberately **not** checked against shortlist membership — see F2 below.
- `agents/ingestion/test_segment.py`: 23 tests covering both well-formed
  `topic_action` values, prompt-content assertions, `LLMError` passthrough,
  and every malformed-output case (unparseable JSON, non-object JSON, each
  of the 6 missing fields, each wrong-typed field, a wrong list-element
  type, an invalid `topic_action` value, both cross-field-inconsistency
  cases).

## Impact

- `agents/ingestion/segment.py` is purely additive — no existing module
  modified (`git show --name-status` confirms only `segment.py` +
  `test_segment.py` touched). `agents/ingestion/propose_split.py` (3.4.5)
  and any RPC wiring (3.4.4) are not built yet and remain out of scope here.
- Full `agents/` regression suite (`pytest agents/ -q`) run 3x: 106/106
  passing each run, zero flakiness. Targeted suite: 23/23 passing. `ruff
  check` clean on both new files.
- Two non-blocking findings, disclosed and recorded in
  `.cdr/index/regression.jsonl` and `.cdr/memory/pending.md`:
  - **F1 (medium) — forward to 3.4.4 and especially 3.4.6**: LLM responses
    wrapped in markdown code fences (` ```json ... ``` `) are rejected
    outright as unparseable JSON (`_parse_segment_json` calls `json.loads`
    directly with no fence-stripping), even though real Ollama-backed models
    frequently wrap responses this way despite the prompt's explicit "no
    markdown code fences" instruction. Experimentally verified during
    verification (not just theoretical): a fenced valid-JSON payload raises
    `SegmentParseError` instead of returning a `SegmentResult`. This is a
    genuine production risk once `segment()` is wired to a live model.
    **Any implementation dispatch for 3.4.4 (PutSegment wiring) and
    especially 3.4.6 (live-Ollama smoke test) should be made explicitly
    aware of this gap** and either fix it defensively (strip leading/trailing
    fences before `json.loads`, with a regression test) or explicitly
    re-confirm it's still an acceptable deferred risk before closing #18.
  - **F2 (low) — forward to 3.4.4**: `target_topic` is not validated against
    real shortlist/catalog membership. By design: `shortlist()`'s own
    contract (3.4.2) is a bounded, re-ranked subset, not an exhaustive
    membership list, so a strict membership check here would incorrectly
    reject a legitimate existing topic the shortlist happened not to
    surface. The correct enforcement point is 3.4.4 (PutSegment wiring),
    which has access to the real catalog — 3.4.4's acceptance criteria
    should explicitly cover "reject/handle `target_topic` not found in
    catalog."

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-10/007-verification`
- Zero must-fix findings; two non-blocking findings (F1, F2) as detailed
  above, both independently re-derived/experimentally confirmed by the
  verifier (not merely taken on the implementer's word).

## Release Notes

- Added `agents/ingestion/segment.py` (`segment()`, `SegmentResult`,
  `SegmentError`, `SegmentParseError`) and
  `agents/ingestion/test_segment.py`: turns a document + bounded topic
  shortlist into a structured, validated segmentation decision via an LLM
  call, per `docs/LLD/ingestion-agent.md`'s Segmentation agent output shape.
  Third of 6 subtasks toward issue #18 (segmentation agent, milestone #5,
  Phase 3); **3 subtasks remain (3.4.4-3.4.6)** before issue #18 can close.
- Non-blocking follow-up flagged forward: markdown-code-fence-wrapped LLM
  JSON responses are currently rejected rather than parsed (F1) — relevant
  to 3.4.4 and critical to check before/during 3.4.6's live-Ollama smoke
  test. `target_topic`-vs-catalog validation is deferred by design to 3.4.4
  (F2).
