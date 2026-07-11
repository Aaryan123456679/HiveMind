# Requirement — Issue #45 (batched low-priority cleanup)

Source: `gh issue view 45` (fetched verbatim, not paraphrased from summary).

Four low-severity, non-blocking findings from Phase 3 (issue #18) verification,
explicitly marked "safe to batch into a single low-priority cleanup task" and
following milestone #10's existing one-issue-per-low-severity-batch convention
(issues #38-#42). No numbered subtasks in the issue body -> implement as ONE
CDR run.

- **F2**: `agents/ingestion/propose_split.py::_char_offset_to_byte_offset` — UTF-8
  conversion logic is correct but has no checked-in non-ASCII test coverage
  (existing fixtures are pure ASCII). Add a non-ASCII fixture (café / überall /
  emoji) regression test.
- **F3**: `agents/ingestion/propose_split.py` — a substring-marker near-miss
  (a marker string that is itself a substring of an earlier marker) produces a
  structurally-valid-but-semantically-odd split. Matches the module's own
  disclosed guarantee (no gaps/overlaps in construction) but is untested. Add a
  regression test capturing this behavior.
- **F5**: `engine/rpc/server_test.go::TestPutEdgeAndEntityHandlers` — the
  weight-increment subtest calls `PutEdge` three times with identical weights
  (1, 1, 1), which cannot discriminate "sum" semantics (3) from "count"
  semantics (also 3, coincidentally, with all-1s) in `graph.Compact`/`mergeEdges`.
  Strengthen using distinct weights (3+4+5=12) so the assertion only passes
  under true summing semantics.
- **F6**: `agents/ingestion/wiring.py::SegmentWiringClient.put_edge`'s
  `weight_delta` parameter name is misleading — RPC semantics are "this call's
  own occurrence weight" (summed later server-side by `graph.Compact`), not a
  running delta/total. Rename to `occurrence_weight` for clarity (pure rename,
  no behavior change) and update the one call site (`execute_segment`,
  `weight_delta=1`).

Acceptance criteria: each finding addressed with a test (F2, F3, F5) or a safe
rename with call-site update (F6); no behavior change to production logic for
F2/F3/F5 (test-only); F6 is a mechanical rename verified via grep for all
usages of `weight_delta`.
