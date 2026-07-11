# Requirement -- task-4.6.3.1 (issue #56, milestone #10)

Source: GitHub issue #56 ("agents/query + api: real gRPC/HTTP wiring for /query route, plus
e2e expansion-branch coverage gap"), a follow-up to issue #25 (milestone #6), carrying
forward 3 non-blocking findings disclosed during 4.6.1/4.6.2 verification:

- F-4.6.1-1: `/query` route has no real gRPC/HTTP wiring; `api/main.go` wires in
  `notImplementedPipeline`. No real gRPC client connects `api/` (Go) to
  `agents/query/pipeline.py` (Python), and no real implementations exist for the
  `search_candidates`/`graph_neighbors`/`get_file` callables `run_query_pipeline` takes.
- F-4.6.1-2: `GetFileFn`'s shape (`file_id -> (path, content)`) has no proto counterpart --
  the real `GetFileResponse` message has only `content`/`version`, no `path`. Real wiring
  must source `path` from `TopicCandidate.path` (already present on candidates returned by
  `search_candidates`) instead of expecting `get_file` to supply it.
- F-4.6.2-1: e2e topic-expansion coverage gap -- `test_query_e2e.py` uses `k=1`, so
  `topic_selector.expand_insufficient_topics`'s expansion branch is only exercised
  in-memory (`test_pipeline.py`, `k=2`), never end-to-end on-disk.

## Scope decomposition for this run

The full issue is the largest remaining milestone-#10 subtask and spans two languages
(Go `api/` + Python `agents/query/`) plus a proto/service-boundary extension for the
Go->Python leg (F-4.6.1-1's route-level wiring). Per the dispatching agent's explicit
instruction, this is too large for one commit and is decomposed into ordered
sub-subtasks:

- **task-4.6.3.1 (this run)**: Fix `GetFileFn`'s proto-shape mismatch (F-4.6.1-2) in
  `agents/query/pipeline.py`, and add real, production-usable gRPC-backed
  `search_candidates`/`graph_neighbors`/`get_file` callable implementations in a new
  `agents/query/wiring.py` (mirroring `agents/ingestion/wiring.py` and
  `agents/ingestion/shortlist.py`'s `GrpcSearchCandidatesClient` precedent), wired against
  the engine's already-real `SearchCandidates`/`GraphNeighbors`/`GetFile` RPCs. This is the
  Python-side half of F-4.6.1-1 that requires no proto changes (those three RPCs already
  exist and are already served by `engine/rpc/server.go`).
- **task-4.6.3.2 (forwarded)**: Extend `proto/hivemind.proto` with a new RPC through which
  `api/`'s Go `/query` route can invoke the Python `run_query_pipeline` (Python as gRPC
  server, mirroring `ProposeSplit`'s reversed direction), regenerate Go+Python stubs, add a
  runnable Python server process, and replace `api/main.go`'s `notImplementedPipeline` with
  a real Go gRPC client. Closes the remainder of F-4.6.1-1.
- **task-4.6.3.3 (forwarded)**: Add/extend an e2e test at `k=2` in `test_query_e2e.py` to
  genuinely exercise topic-expansion + citation-resolution end-to-end on-disk (F-4.6.2-1).

## Acceptance criteria for task-4.6.3.1

- `agents/query/pipeline.py`'s `GetFileFn` no longer expects a caller-supplied `path`;
  `_build_selected_markdown` sources path from the already-known `TopicCandidate.path` for
  every `file_id` reachable from `select_top_k`'s output, with an honestly-disclosed
  fallback for `file_id`s reachable only via graph-neighbor expansion (for which no proto
  message anywhere in `hivemind.proto` carries `path` -- a genuine, forwarded gap, not
  something 4.6.3.1 can fabricate a fix for).
- `agents/query/wiring.py` provides real `GrpcSearchCandidatesClient`,
  `GrpcGraphNeighborsClient`, `GrpcGetFileClient` classes usable as
  `search_candidates`/`graph_neighbors`/`get_file` arguments to `run_query_pipeline`,
  each backed by `hivemind_pb2_grpc.HiveMindStub` over a caller-supplied `grpc.Channel`.
- Existing `agents/query/test_pipeline.py`/`test_query_e2e.py` continue to pass with the
  updated `GetFileFn` shape (content-only), and new tests cover `wiring.py`'s translation
  logic without requiring a live engine (mocked stub, per existing `shortlist.py`/
  `wiring.py` precedent).
