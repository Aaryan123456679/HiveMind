# Issue #18 — Segmentation Agent (milestone #5, "Phase 3") — Consolidated Closure Summary

**Status: all 6 subtasks implemented, verified (PASS or PASS_WITH_COMMENTS),
and committed LOCALLY. NOT pushed. Issue #18 and any GitHub milestone state
have NOT been touched or closed — that requires separate, fresh, explicit
user authorization, which has not yet been sought.**

## Subtask ledger

| Subtask | Summary | Commit(s) | Verdict | Verification run |
|---|---|---|---|---|
| 3.4.1 | `agents/llm/client.py` (`LLMClient` ABC, `LLMError`) + `agents/llm/ollama_client.py` (`OllamaClient`) — provider-agnostic LLM completion interface, first real code in previously scaffold-only `agents/llm/`. | `e7d1e07`, `987b5f6` (metadata-only follow-up) | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/001-verification` |
| 3.4.2 | `agents/ingestion/shortlist.py` — candidate topic shortlisting via `SearchCandidates` (plain btree prefix-scan) + local Okapi BM25 re-ranking. New `GrpcSearchCandidatesClient`. | `98dda16` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/004-verification` |
| 3.4.3 | `agents/ingestion/segment.py` — segmentation prompt + structured JSON output parsing (`segment()` → `SegmentResult`). New `SegmentError`/`SegmentParseError` hierarchy. | `659306f` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/007-verification` |
| 3.4.4a | `engine/rpc/server.go` + proto — added missing `PutEdge`/`PutEntity`/`LookupEntity` RPCs (user-authorized new scope, surfaced mid-verification of 3.4.4, not a numbered GitHub subtask itself; unblocked 3.4.4b). | `8e90334`, `79b5d71` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/012-verification` |
| 3.4.4b | `agents/ingestion/wiring.py` — rewired `SegmentWiringClient`/`GrpcEntityEdgeClient` to call the new real entity/edge RPCs (previously PutSegment-only). | `b796ec5` | PASS | `.cdr/runs/2026-07-10/015-verification` |
| 3.4.5 | `agents/ingestion/propose_split.py` — Python callee-side `ProposeSplit` business logic: deterministic marker-based `SectionRange` resolution, fence-stripping, `LLMError` propagation. Go-side gRPC client was already done in task-3.2.3. | `e744397` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/032-verification` |
| 3.4.6 (final) | Fixture suite (`test_segment_fixtures.py`, `testdata/notes_corpus/`), first end-to-end pipeline-composition test, optional live-Ollama smoke test (`test_segment_live.py`), and closure of forwarded finding F1 via shared `agents/ingestion/_json_fences.strip_code_fences` helper. | `48f1845` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/035-verification` |

Note: 3.4.4 is tracked in the task index as a single logical unit
(3.4.4a + 3.4.4b together closing out the GitHub-numbered subtask), since
3.4.4a was necessary, user-authorized scope to unblock 3.4.4b rather than a
separately numbered issue subtask.

## Findings closed during issue #18

- **F1 (medium)** — `segment.py` rejected markdown-code-fence-wrapped LLM
  JSON output instead of parsing it. Surfaced during 3.4.3 verification,
  proactively worked around (not fixed at the source) by 3.4.5's own
  fence-stripping, and finally **CLOSED** by 3.4.6 via the shared
  `_json_fences.strip_code_fences` helper now used by both `segment.py` and
  `propose_split.py`. See `.cdr/index/regression.jsonl`
  (`hivemind-issue18-3.4.3-segment-json-parsing`, now `status: resolved`)
  and `.cdr/memory/pending.md`.

## Still-open, carried-forward findings — MUST be surfaced to the user before any push/close decision

None of the following are blocking issue #18's own literal acceptance
criteria, but all remain genuinely open and should be explicitly presented
to the user alongside any push/close authorization request:

- **F2 (low, test-coverage gap)** — `agents/ingestion/propose_split.py`'s
  `_char_offset_to_byte_offset` UTF-8 char→byte offset conversion
  (load-bearing: `proto/hivemind.proto`'s `SectionRange` is a byte-offset
  contract) is untested by any non-ASCII fixture in the shipped suite;
  manually confirmed correct by the verifier via ad hoc repro, but not
  pinned by a checked-in test. From 3.4.5 verification
  (`.cdr/runs/2026-07-10/032-verification`).
- **F3 (informational)** — `propose_split.py`'s `_resolve_section_ranges`
  monotonic forward `str.find` can, in a substring-marker near-miss
  scenario, resolve to a structurally valid but semantically nonsensical
  boundary; untested. Not a spec violation. From 3.4.5 verification.
- **F4 (HIGH severity, pre-existing, NOT introduced by issue #18)** —
  `engine/rpc/server.go`'s `PutSegment` CREATE path never sets
  `catalog.CatalogRecord.PathHash`, so a newly created topic is never
  discoverable by path via `SearchCandidates` afterwards. Pre-existing since
  task-3.2.2; correctly disclosed as out-of-scope by every issue #18
  subtask that touched adjacent code (3.4.4/3.4.4a/3.4.4b), but never fixed.
  This is the single highest-severity item in this list and blocks real
  discoverability of any topic created through the pipeline this issue just
  finished wiring end-to-end — worth flagging prominently.
- **F5 (low, test-quality gap, not a functional defect)** —
  `engine/rpc/server_test.go`'s `PutEdge` weight-summing subtest
  (`PutEdge_WeightIncrement_ViaCompact`) uses identical weight=1 for all 3
  calls, so it cannot distinguish genuine summation from an
  occurrence-counting bug. The verifier independently confirmed real
  summation via an ad hoc non-uniform-weight test; the shipped test itself
  just doesn't prove it. From 3.4.4a verification.
- **F6 (low, naming/terminology)** — `SegmentWiringClient.put_edge`'s
  `weight_delta` parameter name is misleading: it maps directly onto
  `PutEdgeRequest.weight` (an occurrence weight, not a delta/running-total),
  and `execute_segment` always passes `weight_delta=1` today so there is no
  current behavioral bug — but the name invites future misuse. From 3.4.4b
  verification.

All of F2-F6 are recorded in `.cdr/index/regression.jsonl` and
`.cdr/memory/pending.md`, and per this repo's standing convention are
slated for eventual folding into GitHub milestone #10 ("Phase 4.5:
Storage-engine technical debt & correctness follow-ups", issues #38-42).
None has a dedicated GitHub issue of its own yet.

## Explicit next-step gate

Issue #18 is code-complete and verified locally. Before any `git push` or
any GitHub issue/milestone state change (closing #18, updating milestone
#5), the user should be asked to authorize that step explicitly, and should
be shown this findings list — especially F4 — first.
