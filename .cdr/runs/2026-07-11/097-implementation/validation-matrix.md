# Validation matrix -- task-4.6.3.1

| # | Requirement | Test | Status |
|---|---|---|---|
| 1 | `GetFileFn` no longer requires a path from the caller | `test_pipeline.py::test_run_query_pipeline_calls_agents_in_order`, `::test_run_query_pipeline_response_shape` -- `fake_get_file` now returns `str` only | PASS |
| 2 | Path is sourced from `TopicCandidate.path` for ids reachable from `select_top_k` | `test_run_query_pipeline_response_shape` -- asserts `"## File: billing/InvoiceDisputes.md"` in the synthesis prompt for file_id=1 (a selected topic) | PASS |
| 3 | Ids reachable only via graph-neighbor expansion get a disclosed placeholder, not a fabricated path | `test_run_query_pipeline_response_shape` -- asserts `"## File: (path unknown; file_id=3)"` for file_id=3 (expansion-only) | PASS |
| 4 | Existing call-order / empty-selection behavior is preserved | `test_run_query_pipeline_calls_agents_in_order`, `test_run_query_pipeline_raises_on_empty_selection` -- unchanged assertions, still pass | PASS |
| 5 | e2e test's real-corpus flow still works with the content-only `get_file` shape | `test_query_e2e.py::test_e2e_valid_citation_resolves_to_real_seeded_file`, `::test_e2e_hallucinated_citation_is_flagged` | PASS |
| 6 | `GrpcSearchCandidatesClient` correctly translates `SearchCandidatesRequest`/`Response` | `test_wiring.py::test_search_candidates_client_translates_request_response` | PASS |
| 7 | `GrpcGraphNeighborsClient` correctly translates `GraphNeighborsRequest`/`Response`, including `EdgeType` enum -> `str` | `test_wiring.py::test_graph_neighbors_client_translates_request_response` | PASS |
| 8 | `GrpcGetFileClient` returns content-only (`str`), matching `pipeline.GetFileFn` exactly | `test_wiring.py::test_get_file_client_translates_request_response`, `::test_get_file_client_is_a_valid_get_file_fn_for_pipeline` | PASS |
| 9 | No regression elsewhere in `agents/` | Full `agents/.venv` pytest suite | PASS (see self-consistency note) |

Environment note: the system/anaconda `python` on this machine has a pre-existing,
unrelated protobuf gencode/runtime version mismatch (`gencode 6.33.5` vs `runtime 5.29.6`)
that makes *any* `hivemind_pb2`-importing test fail, including pre-existing
`agents/ingestion/test_shortlist.py`'s own grpc-client tests -- confirmed this is not
introduced by this run. `agents/.venv` (the repo's own pinned environment, per the recent
"pin protobuf explicitly" commit) does not have this problem; all commands above were run
via `agents/.venv/bin/python`.
