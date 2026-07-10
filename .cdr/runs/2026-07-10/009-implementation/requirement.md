# Requirement — issue #18 subtask 3.4.4

Wiring: execute segments via `PutSegment`; `entities` -> `ENTITY_COOCCUR` edges;
`related_topics` -> `LLM_ASSERTED` edges.

## Acceptance criteria (verbatim from `gh issue view 18`)

Each parsed segment's APPEND_EXISTING/CREATE_NEW action is executed via the engine's
`PutSegment` RPC; entities feed `entity.idx` and increment `ENTITY_COOCCUR` edge
weights; `related_topics` create `LLM_ASSERTED` edges.

## Test spec

`pytest agents/ingestion/test_segment_wiring.py` (engine RPC client mocked): assert
correct `PutSegment` calls and correct edge-creation calls for a fixture segment set.

## Impacted modules (per issue)

`agents/ingestion/wiring.py`, `agents/ingestion/test_segment_wiring.py`.

## Non-goals / explicitly out of scope for this subtask

- No changes to `proto/hivemind.proto`, `engine/rpc/server.go`, or any Go code.
- No new RPCs added to the wire contract (issue #16's task-3.2.1 froze the proto at
  exactly six RPCs; see `docs/LLD/rpc.md`).
- 3.4.5 (`ProposeSplit`) and 3.4.6 (fixture/live suite) are separate subtasks.

## Forwarded findings from 3.4.3 (must be addressed here)

- **F2**: `target_topic` (a path string) is not validated against the real
  catalog by `segment()` (3.4.3) by design — 3.4.4 is the layer with access to a
  real resolver and must reject/handle an unresolvable `target_topic`.
- **F1**: markdown-code-fence-wrapped LLM JSON is rejected by `segment()`'s parser.
  Not directly this subtask's concern (wiring consumes an already-parsed
  `SegmentResult`), but re-forwarded to 3.4.6 since it remains open.
