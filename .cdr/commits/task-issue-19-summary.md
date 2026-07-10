# Issue #19 (dataset ingestion epic) -- consolidated closure summary

Issue #19 comprised 2 subtasks. Both are now independently implemented,
verified, and committed **locally only**.

| Subtask | Summary | Commit(s) | Verdict | Verification run |
|---|---|---|---|---|
| 3.5.1 | `data/load_bitext.py` + `data/load_enron.py` dataset loaders, mapping a real downloaded 30-row Bitext HF sample and a disclosed hand-authored Enron-format fixture sample onto `agents/ingestion`'s existing `normalize_ticket`/`normalize_email` normalizer inputs. | `4883fc7`, `7c563ca` (docs closure follow-up) | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/038-verification` |
| 3.5.2 (final) | Full end-to-end ingestion smoke run: a genuine real Enron sample streamed from the official CMU/FERC archive, a standalone real Go gRPC smokeserver, and the pipeline exercised end-to-end via real Ollama + real gRPC. | `9794fd8` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-10/041-verification` |

## Closure note

Issue #19 is genuinely complete for its stated scope: both subtasks were
independently implemented and then independently re-verified (not merely
self-reported) against the real, non-mocked artifacts each subtask
required -- a real downloaded/streamed dataset sample in both cases, and
a real standalone gRPC server + real local LLM for 3.5.2. No changes were
made to any pre-existing production file outside `data/`,
`agents/ingestion/`, and the new `engine/cmd/smokeserver/` binary; the
segmentation/proposal/shortlist core logic from issue #18 was left
untouched throughout.

This is **local-only**. Nothing has been pushed, and issue #19's
issue/milestone state on GitHub has not been touched. Closing issue #19
(and, if applicable, milestone #5 "Phase 3") requires separate, fresh,
explicit user authorization.

## Still-open findings carried forward (for the user's awareness before any push/close decision)

- **F2** (low, issue #18, `segment.py`'s `target_topic` not validated
  against shortlist/catalog membership) -- disclosed, intentional scope
  boundary; not fixed, not blocking.
- **F3** (low, issue #18, `propose_split.py`'s substring-marker near-miss
  case untested against real-world LLM near-miss patterns) -- not fixed,
  not blocking.
- **F4** (HIGH severity, non-blocking, now confirmed by 3.5.2's
  verification as requiring a proto/wire-contract change): `PutSegment`'s
  CREATE path never sets `catalog.CatalogRecord.PathHash`, because
  `PutSegmentRequest` carries no path field at all. This subtask's
  end-to-end smoke test directly observed the real consequence (newly
  created files are not discoverable via `SearchCandidates`). Recommend a
  dedicated future task to add the missing field, regenerate both Go and
  Python codegen, update the handler and both clients in `wiring.py`, and
  add a regression test proving discoverability after the fix.
- **F5** (low, issue #18, `PutEdge` weight-summing test uses uniform
  weights and cannot distinguish sum-of-weights from count-of-calls) --
  independently confirmed as a genuine but non-blocking test-coverage gap.
- **F6** (low, issue #18, `weight_delta` naming/semantics in
  `SegmentWiringClient.put_edge`/`GrpcEntityEdgeClient.put_edge`) -- not
  fixed, not blocking.
- **F7** (new, non-blocking, medium severity, issue #19 3.5.2): real
  local `llama3.1:8b` responses can contain raw control characters/stray
  triple-quote artifacts inside JSON string values, distinct from the
  already-closed F1 (markdown-code-fence wrapping). Affects ~7/11 real
  documents in this run's smoke test. `segment.py`'s parser was
  intentionally left untouched -- out of this subtask's scope.

All 7 findings above (F2-F7, with F1 already closed) are recorded in
`.cdr/index/regression.jsonl` and `.cdr/memory/pending.md`. None of them
block issue #19's closure for its own stated scope, but all should be
surfaced to the user before deciding whether to push and/or close issue
#19 (and any related milestone) on GitHub.
