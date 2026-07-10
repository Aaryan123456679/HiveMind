# task-3.4.4: PutSegment wiring + entity/edge creation (issue #18, subtask 3.4.4) -- fully closed

## Summary

Issue #18 subtask 3.4.4 required wiring a segmented document (`SegmentResult`, from
3.4.3) through to the Go engine: execute the `PutSegment` write, then feed `entities`
into `entity.idx` and increment `ENTITY_COOCCUR` graph edges, and turn `related_topics`
into `LLM_ASSERTED` graph edges (per `docs/LLD/ingestion-agent.md`'s "What the Go engine
does with each segment").

The `PutSegment`-wiring half of this was already real and committed at `ae099571`
(`GrpcPutSegmentClient`/`execute_segment`'s fail-fast semantics). Mid-verification of
the original 3.4.4 implementation, the entity/edge half surfaced a genuine blocker
(finding F3, escalation required): `proto/hivemind.proto` had no RPC for edge-writes or
an entity index at all -- `SegmentWiringClient`'s `lookup_entity_files`/`index_entity`/
`put_edge` were Protocol-only stubs with nothing real behind them. This was disclosed
rather than silently worked around, and resolved via a user-authorized two-part path
instead of forcing an incomplete subtask closure:

- **3.4.4a** (`task-3.4.4-engine-edge-rpc`, commits `8e90334`/`79b5d71`): added the
  missing `PutEdge`, `PutEntity`, `LookupEntity` RPCs to the engine (proto + Go
  handlers), giving the system real backing for graph-edge writes and an entity-name
  index for the first time.
- **3.4.4b** (commit `b796ec5`): rewired `agents/ingestion/wiring.py`'s
  `SegmentWiringClient` to call those three new RPCs for real (`GrpcEntityEdgeClient`,
  composed with the pre-existing `GrpcPutSegmentClient` into `GrpcSegmentWiringClient`),
  with `execute_segment`'s fail-fast (`PutSegment`) vs. best-effort-with-error-collection
  (entity/edge) logic byte-for-byte unchanged.

Together, 3.4.4a and 3.4.4b close out issue #18 subtask 3.4.4 for good. This record
consolidates both halves into a single closure document; `task-3.4.4-engine-edge-rpc.md`
remains the detailed record of 3.4.4a's own engine-side implementation.

## Features

- **`PutSegment` wiring** (pre-existing, `ae099571`): `execute_segment` calls the real
  `PutSegment` RPC via `GrpcPutSegmentClient`, aborting fail-fast on an unresolvable
  `target_topic` or a transport failure before any content write happens.
- **New engine RPCs (3.4.4a)**: `PutEdge` (appends one raw graph-edge occurrence to
  `engine/graph`'s per-source-node edge log, leaving weight-summing/dedup to the
  existing `Compact` pass), `PutEntity` (registers an entity-name -> fileID association
  in a dedicated `entity.idx` B+Tree, kept separate from the path index), and
  `LookupEntity` (prefix-scans that index back out by entity name). Purely additive to
  `proto/hivemind.proto` -- none of the original six RPCs were renumbered or changed.
- **Real Python rewiring (3.4.4b)**: `GrpcEntityEdgeClient` wraps `LookupEntity`/
  `PutEntity`/`PutEdge` over a caller-supplied `grpc.Channel`, following
  `GrpcSearchCandidatesClient`'s lazy-import convention exactly. `GrpcSegmentWiringClient`
  composes it with `GrpcPutSegmentClient` so `SegmentWiringClient`'s full Protocol
  (`put_segment`, `lookup_entity_files`, `index_entity`, `put_edge`) is real-RPC-backed
  end to end, with zero Protocol signature changes. Test doubles remain available and
  are still what `execute_segment`'s own unit tests use, per the issue's original test
  spec ("engine RPC client mocked").
- Cross-file `ENTITY_COOCCUR` semantics (disclosed deviation from a literal pairwise
  reading, justified against `docs/LLD/graph.md`'s more specific "incremented ... across
  files" wording) and `target_topic` pre-RPC resolution (closing 3.4.3's forwarded F2)
  were both already implemented as part of the original `PutSegment`-wiring work and are
  unchanged by 3.4.4a/3.4.4b.

## Impact

- `SegmentWiringClient` is no longer Protocol-only for any of its four methods --
  `agents/ingestion/wiring.py` now has a fully real, gRPC-backed production
  implementation for the complete entity/edge/segment write path issue #18 originally
  specified.
- Scope stayed tightly contained throughout: 3.4.4a touched only `proto/`, `engine/rpc/`,
  and regenerated stubs; 3.4.4b touched only `agents/ingestion/wiring.py` and its test
  file. Neither touched `ProposeSplit` (3.4.5) or the pre-existing `PathHash` gap (F4).
- Combined test evidence: engine side 120/120 (`go test ./engine/... -race`), Python
  side 127/127 (up from the pre-3.4.4a baseline of 120, +7 new tests in 3.4.4b), `ruff`
  clean.
- Issue #18 (segmentation agent, milestone #5, Phase 3) now has 2 subtasks remaining:
  3.4.5 (`ProposeSplit`) and 3.4.6 (live-Ollama smoke test). Issue #18 itself is **not**
  closed by this record.

## Verification

- **3.4.4a** -- verdict: `PASS_WITH_COMMENTS`, run: `.cdr/runs/2026-07-10/012-verification`
  (commits `8e90334`/`79b5d71`).
- **3.4.4b** -- verdict: `PASS`, run: `.cdr/runs/2026-07-10/015-verification`
  (commit `b796ec5`).

### Non-blocking findings carried forward

- **F4** (high severity but non-blocking, pre-existing, `engine/rpc/server.go`,
  task-3.2.2): `PutSegment`'s CREATE path never sets `catalog.CatalogRecord.PathHash`
  when allocating a new file -- a file created via `PutSegment(file_id=0, ...)` is not
  discoverable by path via `SearchCandidates` afterwards. Out of scope for both 3.4.4a
  (proto/engine-RPC addition) and 3.4.4b (`agents/ingestion/wiring.py`-only rewiring) to
  fix; still open. Recorded in `.cdr/index/regression.jsonl` (`F4`).
- **F5** (low severity, `engine/rpc/server_test.go`, 3.4.4a):
  `TestPutEdgeAndEntityHandlers/PutEdge_WeightIncrement_ViaCompact` issues all 3
  `PutEdge` calls with weight=1, so the resulting summed weight of 3 can't disambiguate
  genuine summation from a count-of-occurrences bug from the shipped test alone. The
  verifier independently confirmed genuine summation via an ad hoc distinct-weights test
  (3+4+5=12, not shipped). Recommendation: extend the shipped test with non-uniform
  weights. Recorded in `.cdr/index/regression.jsonl` (`F5`).
- **F6** (low severity, `agents/ingestion/wiring.py`, 3.4.4b):
  `GrpcEntityEdgeClient.put_edge`'s parameter is named `weight_delta` but semantically
  carries this call's own occurrence weight (per the `PutEdge` RPC contract), not a
  delta/running-total. `execute_segment` always passes `weight_delta=1` today so there is
  no current behavioral bug, but the name invites future misuse. Recommendation: rename
  to `weight`/`occurrence_weight` in a future cleanup pass. Recorded in
  `.cdr/index/regression.jsonl` (`F6`).

All three are non-blocking, previously disclosed, and slated for eventual folding into
GitHub milestone #10 ("Phase 4.5: technical debt & correctness follow-ups"); no
dedicated GitHub issue is created for them now.

## Release Notes

Issue #18 subtask 3.4.4 ("PutSegment wiring + entity/edge creation") is now fully
complete. `agents/ingestion/wiring.py`'s `SegmentWiringClient` writes segmented document
content via `PutSegment`, indexes entities and increments cross-file `ENTITY_COOCCUR`
edges, and creates `LLM_ASSERTED` edges for related topics -- all via real gRPC calls to
the engine, with no remaining Protocol-only stubs on this path. This closure combines two
prior, separately-verified pieces of work: the engine-side `PutEdge`/`PutEntity`/
`LookupEntity` RPCs (3.4.4a) and the Python-side rewiring that calls them (3.4.4b). Three
non-blocking findings (F4, F5, F6) are carried forward and tracked for a future
technical-debt pass; none block production use of this path. Issue #18 remains open with
3.4.5 (`ProposeSplit`) and 3.4.6 (live-Ollama smoke test) as the next subtasks.
