# task-3.4.5: ProposeSplit — real Python callee-side split-proposal logic

## Summary

Adds `agents/ingestion/propose_split.py`'s `propose_split()` function: the
fifth of 6 subtasks in GitHub issue #18 (segmentation-agent epic, milestone
#5, "Phase 3"). This subtask closes the **Python side** of the `ProposeSplit`
RPC only. The **Go-side gRPC client** (`engine/split/proposer_grpc.go`,
calling `hivemindv1.HiveMindClient.ProposeSplit` against this service) was
already implemented in an earlier task, 3.2.3. `proto/hivemind.proto`'s own
top comment and `docs/LLD/rpc.md`'s "Status" note both confirm the split: the
Go engine is the gRPC *client* for this one RPC; `engine/rpc/server.go`'s
handler deliberately, permanently stays the generated `Unimplemented` stub,
since the real server-side (callee) business logic belongs here, in
`agents/ingestion/`. `git diff` from this subtask's parent commit confirms
zero Go/proto files touched — no Go-side work was silently skipped.

Given the full content of an over-threshold file, `propose_split()` decides a
topic-coherent split into multiple new files plus a human-readable redirect
summary, via an `LLMClient` (from 3.4.1) call. Issue #18 has **1 subtask
remaining** (3.4.6); this is not a closure commit.

## Features

- **Marker-based section resolution guaranteeing no gaps/overlaps by
  construction**: the LLM is asked only for each section's target topic path
  plus a short verbatim `start_marker` substring — never an exact byte
  offset, which LLMs cannot reliably compute. `_resolve_section_ranges`
  locates each marker deterministically via a monotonic forward `str.find`
  from the previously resolved boundary, and builds `SectionRange`s so the
  first section always starts at byte `0`, the last always ends at
  `len(file_content)`, and every interior boundary is exactly the next
  section's resolved marker offset. This makes the partition invariant hold
  *by construction* regardless of what the LLM said; any unresolvable or
  out-of-order marker raises `ProposeSplitParseError` explicitly rather than
  producing a silently wrong offset.
- **Fence-stripping, proactively avoiding `segment.py`'s open F1**:
  `agents/ingestion/segment.py` (3.4.3) has an open, non-blocking finding
  (F1) where markdown-code-fence-wrapped LLM JSON is rejected outright
  instead of parsed — a real production risk with Ollama-backed models.
  `propose_split.py`'s parser strips a single leading/trailing code fence
  (` ```(json)? ... ``` `) before `json.loads`, proactively avoiding that
  same failure mode. `segment.py` itself is unmodified (out of this
  subtask's file scope); its own F1 remains open, flagged forward again.
- **`LLMError` propagation**: mirrors `segment.py`'s exception design.
  `ProposeSplitError` (base) is deliberately *not* a subclass of
  `llm.client.LLMError` — provider-call failure and unusable-output-after-a-
  successful-call are distinct failure classes a caller needs to
  distinguish. `LLMError` raised by `llm_client.complete()` propagates
  unwrapped through `propose_split()`.
- `SectionRange`/`SplitFileProposal`/`ProposeSplitResult` mirror
  `proto/hivemind.proto`'s `SectionRange`/`SplitFileProposal`/
  `ProposeSplitResponse` field-for-field. One `SectionRange` per proposed
  file (a disclosed simplification — the proto technically allows scattered
  multi-range files per output, not attempted here).
- `agents/ingestion/test_propose_split.py`: targeted test suite covering
  well-formed splits, `LLMError` passthrough, malformed-JSON/missing-field/
  wrong-type cases, too-few-sections, duplicate-path, out-of-order/
  unresolvable markers, and code-fence stripping.

## Impact

- Purely additive: only `agents/ingestion/propose_split.py` and
  `agents/ingestion/test_propose_split.py` added; `git diff` confirms zero
  Go/proto files touched (confirming the architectural claim above, not
  merely asserting it).
- Full `agents/` regression suite (`agents/.venv/bin/pytest agents/ -q`):
  146/146 passing. `ruff check` clean on both files (after this close-out's
  lint fix, below).
- **Lint fix folded into this close-out**: verification flagged a trivial
  F401 (unused `SplitFileProposal` import in
  `agents/ingestion/test_propose_split.py`) — removed as part of this
  commit; confirmed `ruff check` clean and the full suite still 146/146
  passing after the removal.
- Two non-blocking findings carried forward from verification (recorded in
  `.cdr/index/regression.jsonl` and `.cdr/memory/pending.md`):
  - **F2 (low)**: `_char_offset_to_byte_offset`'s UTF-8 char→byte offset
    conversion (load-bearing: `proto/hivemind.proto`'s `SectionRange` is a
    byte-offset contract) is exercised only by pure-ASCII fixtures in the
    shipped suite, where char and byte offsets are numerically identical —
    masking any potential off-by-one. The verifier manually confirmed the
    conversion is correct on multi-byte fixtures (accented characters,
    emoji) via ad hoc repro, but this is not covered by any checked-in test.
  - **F3 (informational)**: the monotonic forward `str.find` in
    `_resolve_section_ranges` can, in a substring-marker near-miss (a later
    section's marker is textually contained within an earlier section's own
    marker match), resolve to a boundary strictly inside the previous
    section's marker text — structurally valid (no gap/overlap) but
    semantically nonsensical. Not a spec violation (consistent with the
    module's own disclosed "partition holds by construction" guarantee), but
    untested by any checked-in test.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-10/032-verification`
- Architectural claim (Go-is-client / Python-is-callee) independently
  re-derived from four separate source artifacts plus an empty
  `git diff -- engine/ proto/`, not taken on the implementer's word.
  Zero must-fix findings; F2/F3 above are the sole non-blocking findings,
  plus the now-fixed F401 lint nit.

## Release Notes

- Added `agents/ingestion/propose_split.py` (`propose_split()`,
  `SectionRange`, `SplitFileProposal`, `ProposeSplitResult`,
  `ProposeSplitError`, `ProposeSplitParseError`) and
  `agents/ingestion/test_propose_split.py`: the Python callee-side business
  logic for the `ProposeSplit` RPC, closing out issue #18's fifth of 6
  subtasks. The Go-side gRPC client for this RPC was already shipped in an
  earlier task, 3.2.3 — this commit does not touch Go or proto files.
- Removed an unused `SplitFileProposal` import (F401) from
  `agents/ingestion/test_propose_split.py` as part of this close-out.
- Non-blocking follow-ups flagged forward: UTF-8 byte-offset conversion
  untested by non-ASCII fixtures (F2), substring-marker near-miss case
  untested (F3).
- Issue #18 has **1 subtask remaining**: 3.4.6 (fixture suite + optional
  live-Ollama smoke test), which must also address 3.4.3's still-open F1
  (markdown-code-fence JSON rejection in `segment.py` itself).
