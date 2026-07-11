# Plan — issue #43, commit 3/3

1. `agents/ingestion/wiring.py`:
   - `SegmentWiringClient.put_segment`: add `path: str` param to the Protocol
     method signature + docstring note (path is the real topic path; ignored
     server-side on append, mirrors proto comment).
   - `execute_segment`: compute
     `path_arg = segment_result.new_topic_path if segment_result.topic_action == "CREATE_NEW" else segment_result.target_topic`
     before the `put_segment` call; pass it as the 3rd arg.
   - `GrpcPutSegmentClient.put_segment(self, file_id, content, path)`: build
     `hivemind_pb2.PutSegmentRequest(file_id=file_id, content=content, path=path)`.
   - `GrpcSegmentWiringClient.put_segment(self, file_id, content, path)`:
     delegate `self._put_segment_client.put_segment(file_id, content, path)`.
   - Update the module docstring's now-stale "new_topic_path cannot be
     registered anywhere queryable today" disclosure section to reflect that
     this is now resolved (issue #43 commit 3/3).

2. `agents/ingestion/test_segment_fixtures.py`:
   - `_FakeWiringClient.put_segment_calls`: `list[tuple[int, bytes, str]]`.
   - `_FakeWiringClient.put_segment(self, file_id, content, path)`: append
     `(file_id, content, path)`.
   - Update the one `put_segment_calls ==` assertion to include the path.

3. `agents/ingestion/test_segment_wiring.py`:
   - `_FakeWiringClient.put_segment_calls`: 3-tuple, same pattern.
   - `_FakeWiringClient.put_segment(self, file_id, content, path)`: append
     3-tuple.
   - Update the 5 existing `put_segment_calls ==` assertions to include path
     (`"a/b"` for CREATE_NEW case, `"billing/InvoiceDisputes"` for
     APPEND_EXISTING cases, segment_result's own path for the failure-path
     tests).
   - Update `test_grpc_put_segment_client_translates_request_response`: call
     `client.put_segment(0, b"content bytes", "docs/new-topic.md")`; assert
     `fake_pb2.PutSegmentRequest.assert_called_once_with(file_id=0,
     content=b"content bytes", path="docs/new-topic.md")`.
   - Update `test_grpc_segment_wiring_client_delegates_all_four_methods` and
     `test_grpc_segment_wiring_client_satisfies_execute_segment_end_to_end` to
     pass/exercise path.
   - Add new test(s) proving path is populated correctly end-to-end through
     `execute_segment` -> `GrpcSegmentWiringClient` -> mocked stub, for both
     `CREATE_NEW` (path == segment's `new_topic_path`) and `APPEND_EXISTING`
     (path == segment's `target_topic`), asserting on
     `fake_pb2.PutSegmentRequest.call_args`.

4. Run `pytest agents/ingestion -k "wiring or segment"` and confirm all green
   (self-consistency, not verification).

5. One commit: `fix(ingestion): populate PutSegment.path from real topic path
   (issue #43, 3/3)`, Problem/Solution/Impact body.
