# Impact analysis -- task-4.6.3.1

## Files touched

- `agents/query/pipeline.py` -- `GetFileFn` type changed from `Callable[[int], tuple[str, str]]`
  to `Callable[[int], str]`; `_build_selected_markdown` gains a `path_by_id` parameter;
  `run_query_pipeline` builds `path_by_id` from `select_top_k`'s output before
  `combine_and_cap` runs. No change to `run_query_pipeline`'s own public signature (still
  takes `search_candidates`/`graph_neighbors`/`get_file` as plain callables) or to
  `QueryPipelineResult`'s shape.
- `agents/query/wiring.py` (new) -- `GrpcSearchCandidatesClient`, `GrpcGraphNeighborsClient`,
  `GrpcGetFileClient`; no changes to any other module's public surface.
- `agents/query/test_pipeline.py` -- `_FILE_CONTENT` fixture and all three `fake_get_file`
  definitions updated to the content-only shape; one assertion updated (file_id=3, reachable
  only via graph-neighbor expansion, now asserts the disclosed placeholder header instead of
  a real path it can no longer legitimately have).
- `agents/query/test_query_e2e.py` -- `_make_get_file` updated to the content-only shape;
  docstring's "no wiring.py analogue" claim corrected (wiring.py now exists, but this test
  still doesn't dial a live engine -- see file's updated disclosure). No assertion changes
  needed: this test's `k=1` fixture never exercises the expansion branch (that gap is
  F-4.6.2-1, forwarded to task-4.6.3.3), so every selected `file_id` always has a
  `TopicCandidate.path`.
- `agents/query/test_wiring.py` (new) -- tests for the three new `Grpc*Client` classes.

## Out of scope, forwarded (not touched by this run)

- `api/main.go`, `api/routes/query.go`, `api/routes/query_test.go` -- Go side untouched;
  `notImplementedPipeline` remains in place (task-4.6.3.2's job).
- `proto/hivemind.proto`, `engine/rpc/gen/`, `agents/hivemind_pb2*.py` -- no proto changes;
  `SearchCandidates`/`GraphNeighbors`/`GetFile` RPCs already exist and are unmodified.
- `agents/query/topic_selector.py`, `agents/query/synthesizer.py`,
  `agents/query/intent_refiner.py` -- unmodified; `GraphNeighbor`/`TopicCandidate` dataclass
  shapes are unchanged, this run only consumes them.
- `agents/query/test_query_e2e.py`'s `k=1` -- unchanged; the k=2 expansion-branch e2e test
  (F-4.6.2-1) is forwarded to task-4.6.3.3.

## Blast radius

Python-only, confined to `agents/query/`. No Go files, no proto files, no other Python
package (`agents/ingestion/`, `agents/llm/`, `agents/eval/`) touched or imported
differently. `GetFileFn`'s shape change is backward-incompatible for any caller supplying
the old `(path, content)` tuple shape, but the only such callers in this repo are the three
test fixtures updated in this same commit -- confirmed via repo-wide grep for `GetFileFn`
and `get_file=`.
