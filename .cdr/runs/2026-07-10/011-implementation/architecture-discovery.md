# Architecture discovery

## Real Go infra found (reused, not reinvented)

- `engine/graph/edgelog.go`: `EdgeLog` — per-source-fileID append-only log
  (`OpenEdgeLog`/`AppendEdge`/`ReadNode`/`TruncateNode`), already the documented "general, durable
  landing zone for newly discovered edges of any type". `AppendEdge(sourceFileID, CSREdge)` is the
  exact primitive a `PutEdge` RPC needs to call — it already validates `EdgeType` via
  `graph.ValidEdgeType`.
- `engine/graph/edge.go`: `NewCSREdge`, `ValidEdgeType`, `EdgeTypeName`/`ParseEdgeType` — validated
  edge construction, reused directly rather than building CSREdge literals by hand.
- `engine/graph/compact.go`: `Compact`/`mergeEdges` — **already implements** the exact
  "ENTITY_COOCCUR sums weight across repeated occurrences; every other type is
  last-write-wins/deduplicated" semantics the issue text asks for, at the log-to-CSR-snapshot fold
  step. This means `PutEdge`'s job is only to *append* one raw occurrence (weight given by caller,
  default should be a per-occurrence contribution, not a running total) to the per-node edge log;
  the "increment on repeated calls" semantics is Compact's job, already shipped, not something this
  RPC needs to reimplement. Confirmed by reading `compact_test.go`/`edgelog_test.go` behavior
  described in `compact.go`'s package doc comment (mergeEdges: `EdgeEntityCooccur` sums, others
  last-write-wins).
- `engine/btree`: `Insert`/`PrefixScan`/`Lookup`, plus the concurrency-safe `btree.Tree` wrapper
  (`NewTree`, `Tree.Insert`, `Tree.Root`). Existing `Server.btreeStore`/`btreeRootNodeID` fields are
  documented as **read-only** ("nothing... writes to the B+Tree" — SearchCandidates only reads).
  Reusing that same tree+root for entity-index writes would require Server to safely track a
  mutating root — which is exactly what `btree.Tree` already provides. Decision: give the entity
  index its **own** dedicated `*btree.Tree` field, distinct from the existing read-only
  `btreeStore`/`btreeRootNodeID` pair used for `SearchCandidates`'s path index — this avoids any risk
  of entity keys polluting path-prefix scans and avoids touching the existing (frozen,
  already-verified) `SearchCandidates` read path at all.
- `engine/rpc/server.go`: `Server` struct's existing "optional, nil-valid dependency" convention
  (`btreeStore == nil` -> `SearchCandidates` returns empty) is the precedent followed for the two new
  optional fields (`edgeLog *graph.EdgeLog`, `entityIndex *btree.Tree`): nil is valid, RPC returns a
  clear `Unavailable`/`Internal` error rather than panicking, mirroring existing style.
- `docs/LLD/graph.md` "Edge shape": `{ targetFileID, edgeType, weight, lastUpdated }` — no source
  fileID in the edge itself (source is the per-node log/CSR row key). `PutEdgeRequest` therefore
  needs `source_file_id` (routing key into `EdgeLog`/CSR) plus the edge's own
  `target_file_id`/`edge_type`/`weight` fields, matching `CSREdge` field-for-field via
  `protoEdgeTypeToGraph`/`graphEdgeTypeToProto` (already exist in `server.go`, reused verbatim for
  the new RPC too, not duplicated).
- `docs/LLD/ingestion-agent.md`: "`entities` feed `entity.idx`... `related_topics` become
  `LLM_ASSERTED` edges" — confirms entity.idx is a distinct mechanism from the edge graph, and that
  `LLM_ASSERTED`/`ENTITY_COOCCUR` are both just ordinary `EdgeType` values `PutEdge` already handles
  generically (no entity-specific edge-type branching needed in the RPC layer itself).

## Key design decision: entity.idx as B+Tree entries under a reserved key prefix

No literal on-disk key format for `entity.idx` is specified in the LLD (confirmed by reading
`docs/LLD/graph.md` and `docs/LLD/ingestion-agent.md` in full — both only describe it in prose, "feeds
entity.idx", with no byte layout). Given `engine/btree`'s existing path-prefix-scan design (used
verbatim by `SearchCandidates`) and the task's explicit suggestion to consider reusing it: entity.idx
is implemented as ordinary B+Tree leaf entries whose **key** is
`"\x00entity\x00" + entityName + "\x00" + fileID (base-10, zero-padded to 20 digits)` and whose
**value** (`FileID` field of the entry) is that same fileID. The dedicated `entityIndex` tree
(distinct from the path index) makes the leading-NUL namespacing belt-and-suspenders rather than
load-bearing, but it is kept anyway in case of a future decision to merge trees. Rationale for
"prefix + zero-padded fileID" as the uniqueness suffix (since the B+Tree's `Insert` upserts a single
fileID per key — one entity name alone cannot map to multiple files under this primitive): each
entity-to-file association gets its own unique leaf key, and `PrefixScan(tree, root,
"\x00entity\x00"+entityName+"\x00")` returns every fileID ever associated with that entity, in
ascending fileID order (zero-padding makes lexicographic order match numeric order). Re-registering
the same (entity, fileID) pair twice is a harmless upsert (idempotent).

## Files read in full before writing code

`docs/LLD/graph.md`, `docs/LLD/ingestion-agent.md`, `docs/LLD/rpc.md`, `proto/hivemind.proto`,
`proto/README.md`, `engine/rpc/server.go`, `engine/rpc/server_test.go` (fixture),
`engine/rpc/integration_test.go`, `engine/graph/{edgelog,edge,csr,compact,edge_append}.go`,
`engine/btree/{insert,lookup,scan,node}.go` (node.go read via architecture pass, not shown above).
