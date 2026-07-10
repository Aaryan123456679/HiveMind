# task-3.4.4-engine-edge-rpc: Engine PutEdge/PutEntity/LookupEntity RPCs

## Summary

Adds three additive gRPC RPCs to `proto/hivemind.proto` and their handler
implementations in `engine/rpc/server.go`: `PutEdge` (append one graph-edge
occurrence to a source file's edge log), `PutEntity` (register an
entity-name -> fileID association in a dedicated entity index), and
`LookupEntity` (prefix-scan that index back out for a given entity name).

**This is USER-AUTHORIZED NEW SCOPE, not a pre-planned GitHub subtask.** It
was discovered mid-verification of issue #18 subtask 3.4.4
(`.cdr/runs/2026-07-10/010-verification/`, finding F3, escalation
required): that verification found `agents/ingestion/wiring.py`'s
`SegmentWiringClient.lookup_entity_files`/`index_entity`/`put_edge` are
Protocol-only interfaces with no real RPC backing anywhere in the repo —
`proto/hivemind.proto` was frozen at exactly the six RPCs task-3.2.1's
acceptance criteria named, and no entity-index store or edge-write RPC
existed. This record documents that new, standalone engine-side milestone
on its own terms — it is not itself issue #18 subtask 3.4.4 and does not
close any of issue #18's numbered subtasks. It exists specifically to
**unblock** 3.4.4's Python-side entity/edge wiring: a follow-up task
(**3.4.4b**, not yet done) will rewire `agents/ingestion/wiring.py` to
actually call these new RPCs in place of its current Protocol-only stubs.

## Features

- **`PutEdge` RPC**: appends one raw edge occurrence (source fileID, target
  fileID, `EdgeType`, weight) to `engine/graph`'s per-source-node edge log
  via `EdgeLog.AppendEdge`. Deliberately does **not** implement
  increment/dedup arithmetic itself — that responsibility stays with
  `engine/graph.Compact` (already implemented, task-3.1.3), which sums
  `ENTITY_COOCCUR` weights and last-write-wins-dedupes every other edge type
  when folding the log into a CSR snapshot. Validates non-zero source/target
  fileIDs, a concrete (non-`EDGE_TYPE_UNSPECIFIED`) edge type, and a
  positive weight, rejecting anything else with `codes.InvalidArgument`.
- **`PutEntity` / `LookupEntity` RPCs**: implement the `entity.idx` concept
  `docs/LLD/ingestion-agent.md` describes only in prose, as a dedicated
  `*btree.Tree` deliberately kept wholly separate from the pre-existing
  read-only path-index tree `SearchCandidates` uses — chosen specifically to
  avoid any risk to that already-verified behavior. `PutEntity` upserts an
  `(entityName, fileID)` pair; `LookupEntity` prefix-scans the index for a
  given entity name and returns the associated fileIDs.
- **Entity-index key encoding**: a `\x00`-delimited, zero-padded key scheme
  (entity name + delimiter + fileID) chosen so prefix-scanning by entity
  name cannot bleed across similarly-prefixed names (e.g. `"foo"` vs.
  `"foobar"` do not cross-match) and so `fileID=0` — already excluded
  upstream by the pre-existing `InvalidFileID` guard — never needs special
  handling in the padding scheme.
- **Nil-safety for both new dependencies**: `Server` gained two new
  nil-valid dependencies, `edgeLog *graph.EdgeLog` and
  `entityIndex *btree.Tree`, matching the pre-existing `btreeStore`
  convention exactly — a nil dependency returns `codes.Unavailable` from the
  affected handler rather than panicking.
- **`Root()==0` special-case in `LookupEntity`**: a genuine, necessary fix
  (not a workaround masking a different bug) — `btree.NodeStore.ReadNode(0)`
  explicitly treats node ID 0 as reserved/never-valid, so without this
  guard, a `PrefixScan` against a never-yet-populated entity index would
  surface a spurious `codes.Internal` instead of a correct empty result.
- Regenerated Go (`engine/rpc/gen/`) and Python (`agents/hivemind_pb2*`)
  stubs from the updated `.proto`. Updated `docs/LLD/rpc.md`.
- `TestPutEdgeAndEntityHandlers` (17 subtests): create, weight-increment via
  `Compact`, last-write-wins for non-cooccurrence edge types, every
  invalid-input path, nil-dependency handling, idempotency, multi-file and
  prefix-isolation behavior. `TestRPCIntegration` gained
  `PutEdge_Compact_GraphNeighbors` and `PutEntity_LookupEntity_RoundTrip`
  subtests exercised over a real gRPC connection.

## Impact

- Purely additive at the proto level: all 3 new RPCs were added without
  renumbering or removing any field of the existing 6 RPCs (confirmed by
  diff during verification). Thin-adapter discipline in `server.go` is
  preserved — no new business logic beyond marshaling/validation was added,
  consistent with the file's pre-existing package-doc convention.
- Scope containment confirmed: `agents/ingestion/wiring.py`, the
  `ProposeSplit` implementation, and the pre-existing `PathHash` (F4) fix
  were **not** touched by either commit in this milestone.
- `go build`/`go vet` clean (engine module). Full `engine/...` test suite
  (btree, catalog, graph, mvcc, rpc, split, wal) passes with `-race`, run
  twice with no flakiness. `agents/` pytest suite: 120/120 passing,
  confirming no cross-language regression from the regenerated Python
  stubs.
- Wire shapes for the three new RPCs are plain protobuf scalars
  (`uint64`/`string`/enum/`repeated uint64`) in `agents/hivemind_pb2.pyi`
  and `hivemind_pb2_grpc.py` — nothing Go-specific leaks through, so
  3.4.4b's Python rewiring has a clean, tested surface to call against.
- **Follow-up required, not yet done: task 3.4.4b** will rewire
  `agents/ingestion/wiring.py`'s `SegmentWiringClient` so its
  `lookup_entity_files`/`index_entity`/`put_edge` methods actually call
  `LookupEntity`/`PutEntity`/`PutEdge` instead of remaining Protocol-only
  stubs. Issue #18 subtask 3.4.4 cannot be considered functionally complete
  until 3.4.4b lands.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-10/012-verification`
- Zero must-fix findings. One new non-blocking finding logged:
  - **F5** (`engine/rpc/server_test.go`, low severity): the shipped
    `TestPutEdgeAndEntityHandlers/PutEdge_WeightIncrement_ViaCompact`
    subtest uses weight=1 for all 3 `PutEdge` calls, which alone cannot
    disambiguate true weight-summing from a count-of-occurrences bug (3
    calls of weight 1 sum to 3 either way). The verifier independently
    confirmed genuine summation behavior via a distinct-weights adversarial
    test (3+4+5=12, deleted after running, not shipped), so this is a
    real but non-blocking test-coverage gap, not a functional defect.
    Recommendation: extend the shipped test with non-uniform weights to
    make the assertion load-bearing.

## Release Notes

- Added `PutEdge`, `PutEntity`, and `LookupEntity` gRPC RPCs
  (`proto/hivemind.proto`, `engine/rpc/server.go`) giving the engine a real,
  tested backing for graph-edge writes and an entity-name-to-file index.
  User-authorized new scope surfaced during verification of issue #18
  subtask 3.4.4, not itself a numbered subtask of that issue — it exists to
  unblock 3.4.4's Python-side entity/edge wiring (task 3.4.4b, not yet
  done). Non-blocking follow-up (F5) flagged: shipped weight-summing test
  uses uniform weights and should be extended with distinct weights in a
  future small test-improvement pass.
