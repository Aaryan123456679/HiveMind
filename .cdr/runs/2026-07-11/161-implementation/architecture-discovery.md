# Architecture Discovery — Issue #45

Token order followed: `.cdr/index/*` -> `docs/LLD/*` -> memory/handoffs -> touched
files -> source.

## Index findings
- `.cdr/index/regression.jsonl` line 18 (issue #53 closure) confirms the standing
  convention: batched low-severity findings get folded into a single milestone-#10
  issue, closed with one commit, one regression-index entry per finding resolved.
- `.cdr/memory` narrative (issue #18 F6 entry) already documents the exact F6
  finding: `GrpcEntityEdgeClient.put_edge`'s `weight_delta` param maps directly
  onto `PutEdgeRequest.weight`, which is "the call's own occurrence weight, not a
  delta/running total"; summing is `graph.Compact`'s job. Matches the issue body
  verbatim (independent corroboration, not just re-reading the issue).

## LLD findings
- `docs/LLD/rpc.md:44-48`: `PutEdge` "appends one occurrence of a graph edge ...
  Weight summing is performed later, by `engine/graph.Compact` ... `PutEdge` itself
  does not sum weights." Confirms F5's and F6's premise directly from canonical
  design doc, not just the issue text.
- No LLD section documents `_char_offset_to_byte_offset` or the substring-marker
  edge case specifically (F2/F3) — these are pure test-coverage gaps in
  `propose_split.py`'s own module docstring-disclosed behavior (module docstring's
  "Deterministic partition guarantee" section, lines 32-48), not undocumented
  behavior.

## Touched-file findings (read before editing)
- `agents/ingestion/propose_split.py` — `_char_offset_to_byte_offset` (line 329-334)
  is a 1-line UTF-8-correct `len(text[:n].encode("utf-8"))`. `_resolve_section_ranges`
  (line 280-326) does a monotonic forward `str.find` per marker; a marker that is a
  substring of an earlier, not-yet-passed-over marker text can resolve to an offset
  inside that earlier marker's own span, since `find` only requires
  `idx > boundaries[-1]`, not "at or after the *end* of the previous marker's own
  text". This is exactly the disclosed-but-untested F3 near-miss.
- `agents/ingestion/test_propose_split.py` — existing tests use one fixture
  document (`_FIXTURE_DOCUMENT`, pure ASCII, English prose with `#` headers) and
  `_FakeLLMClient` (mirrors `test_segment.py`). New tests must follow the same
  conventions (module-level fixtures, `_FakeLLMClient`, `pytest.raises` for error
  paths).
- `agents/ingestion/wiring.py` — `weight_delta` appears in 3 places: the
  `SegmentWiringClient` Protocol (line 213-218), `GrpcEntityEdgeClient.put_edge`
  (441-457, maps to `PutEdgeRequest.weight`), `GrpcSegmentWiringClient.put_edge`
  (485-495, delegates). Two call sites inside `execute_segment` (286, 310) pass
  `weight_delta=1` as a keyword arg.
- `agents/ingestion/test_segment_fixtures.py:119` and
  `agents/ingestion/test_segment_wiring.py` (102, 104, 457, 467, 527) — test doubles
  and call sites that also use the `weight_delta` keyword; must be renamed in
  lockstep or they'll break (Protocol conformance / keyword-arg mismatch).
- `engine/rpc/server_test.go:692-728` (`PutEdge_WeightIncrement_ViaCompact`
  subtest) — three `PutEdge` calls all with `Weight: 1`, asserts
  `e.Weight != 3` after `graph.Compact`. Coincidentally 1+1+1 = 3 = count, so
  sum-vs-count cannot be distinguished. No other subtest in this function or
  elsewhere in `server_test.go` uses `TestPutEdgeAndEntityHandlers`'s helper
  `newFixture`/`f.alphaID`/`f.betaID` pattern in a way affected by this change.

## Conclusion
Issue #45 has no numbered subtasks; body explicitly says "safe to batch into a
single low-priority cleanup task." All four findings are addressed in ONE CDR
implementation run, spanning Python (`agents/ingestion/`) and Go
(`engine/rpc/server_test.go`).
