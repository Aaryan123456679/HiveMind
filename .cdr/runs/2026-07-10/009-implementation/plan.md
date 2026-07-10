# Plan — 3.4.4

## `agents/ingestion/wiring.py`

1. `SegmentWiringClient` (Protocol, `runtime_checkable=False`, structural typing only):
   - `put_segment(file_id: int, content: bytes) -> PutSegmentResult` — real RPC, 1:1
     with `proto/hivemind.proto`'s `PutSegmentRequest`/`Response`.
   - `lookup_entity_files(entity: str) -> Sequence[int]` — entity.idx read (no real
     backing RPC exists; Protocol-only, see architecture-discovery.md).
   - `index_entity(entity: str, file_id: int) -> None` — entity.idx write (same caveat).
   - `put_edge(source_file_id: int, target_file_id: int, edge_type: str, *, weight_delta: int = 1) -> None`
     — edge create/increment (same caveat). `edge_type` is a plain string
     (`"ENTITY_COOCCUR"` / `"LLM_ASSERTED"`), mirroring `segment.py`'s plain-string
     convention for LLD-defined literals with no richer element type at the RPC layer.
2. `PutSegmentResult` frozen dataclass: `file_id: int`, `new_version: int`.
3. `TopicResolverFn = Callable[[str], int | None]` — resolves a topic path to a
   fileID, or `None` if unknown. Caller-supplied (e.g. backed by the same shortlist
   candidates already fetched in 3.4.2, or a fresh `SearchCandidates` call) — `wiring.py`
   does not itself call `SearchCandidates`, keeping this module's only RPC dependency
   the one the issue names (`PutSegment`).
4. Exceptions: `WiringError(Exception)` base; `TopicNotFoundError(WiringError)` — raised
   when `topic_action == "APPEND_EXISTING"` and `resolve_topic_file_id(target_topic)`
   returns `None` (closes 3.4.3's forwarded F2: reject at the layer with real catalog
   access). Raised *before* any RPC call (fail-fast, no wasted/partial writes).
5. `SegmentExecutionResult` frozen dataclass: `file_id`, `new_version`,
   `entity_cooccur_edges_created: int`, `llm_asserted_edges_created: int`,
   `errors: tuple[str, ...]` (best-effort-phase failures, never silently dropped).
6. `execute_segment(segment_result, rpc_client, *, resolve_topic_file_id) -> SegmentExecutionResult`:
   - Fail-fast phase (no partial state to lose yet):
     a. `CREATE_NEW` -> `file_id_arg = 0`. `APPEND_EXISTING` -> resolve `target_topic`;
        raise `TopicNotFoundError` if unresolvable.
     b. Call `rpc_client.put_segment(file_id_arg, content_markdown.encode("utf-8"))`.
        Any exception propagates unwrapped (mirrors `segment.py`'s "provider/transport
        failure propagates unwrapped" convention) -- nothing else has happened yet, so
        there is no partial state to reconcile.
   - Best-effort phase (write already durable; individual edge-op failures must not
     roll back or hide the successful write):
     c. For each entity in `dict.fromkeys(segment_result.entities)` (dedup,
        order-preserving): look up co-occurring files via
        `rpc_client.lookup_entity_files(entity)`; for each `other_file_id != file_id`,
        `put_edge(file_id, other_file_id, "ENTITY_COOCCUR", weight_delta=1)`, catching
        and collecting any exception into `errors` (with the entity/file context in the
        message) rather than raising. Then `index_entity(entity, file_id)` (also
        best-effort/collected) so this file is discoverable for *future* segments'
        co-occurrence lookups.
     d. For each related_topic in `dict.fromkeys(segment_result.related_topics)`:
        resolve to a file_id via `resolve_topic_file_id`; if unresolvable, collect a
        descriptive error and skip (do not raise) -- an LLM-asserted relation to a
        not-yet-known topic is a soft/best-effort signal, not a structural precondition
        like `APPEND_EXISTING`'s `target_topic`. Skip self-edges. Otherwise
        `put_edge(file_id, target_id, "LLM_ASSERTED", weight_delta=1)`, same
        catch-and-collect discipline.
     e. Return `SegmentExecutionResult` with counts of edges actually created and the
        full `errors` tuple -- callers can inspect `errors` and decide whether to retry,
        log, or surface to an operator; nothing is silently dropped.

## `agents/ingestion/test_segment_wiring.py`

- `_FakeWiringClient` implementing `SegmentWiringClient` structurally: records every
  `put_segment`/`lookup_entity_files`/`index_entity`/`put_edge` call; configurable
  canned `lookup_entity_files` responses and per-call injectable exceptions (to test
  best-effort collection).
- Tests: CREATE_NEW happy path (file_id=0 call, entities, related_topics all resolve);
  APPEND_EXISTING happy path (resolved file_id, non-zero); APPEND_EXISTING with
  unresolvable target_topic raises `TopicNotFoundError` before any RPC call is made
  (assert zero calls recorded); PutSegment failure propagates unwrapped and no edge
  calls are attempted; entity co-occurrence across two segments (second segment's
  entity lookup returns the first segment's file_id, asserting the `ENTITY_COOCCUR`
  edge call shape); self-file_id skipped for both entity co-occur and related-topic
  edges; unresolvable related_topic collected into `errors`, not raised, and no edge
  call attempted for it; one entity `put_edge` raising an exception is collected into
  `errors` and does not prevent subsequent entity/related-topic edges from being
  attempted (best-effort semantics); dedup of duplicate entities/related_topics in the
  same segment (each looked up/indexed/edged only once).
