# Plan — Issue #45 batched cleanup

1. **F2** — `agents/ingestion/test_propose_split.py`: add
   `_FIXTURE_DOCUMENT_NON_ASCII` (café / überall / emoji, multi-byte UTF-8
   chars) + `_VALID_PAYLOAD_NON_ASCII` with markers spanning the multi-byte
   sequences, and a new test
   `test_propose_split_resolves_byte_offsets_for_non_ascii_content` asserting
   the partition invariant AND that reassembling via byte slices reproduces
   the original bytes exactly (proves `_char_offset_to_byte_offset` is right,
   not just "didn't crash").

2. **F3** — same test file: add
   `test_propose_split_substring_marker_near_miss_produces_odd_but_valid_split`
   using a document where section 2's marker is a substring of section 1's
   marker, both resolvable via forward `str.find`. Document current behavior:
   split succeeds (no exception — it IS structurally valid), but the first
   section ends up shorter than a human would expect (assert the exact,
   surprising boundary), plus re-assert the full partition invariant holds
   even in this odd case.

3. **F5** — `engine/rpc/server_test.go`: change the 3 `PutEdge` calls in
   `PutEdge_WeightIncrement_ViaCompact` to weights 3, 4, 5 (loop unrolled or
   indexed via a slice) and change the expected total from 3 to 12, updating
   the comment accordingly.

4. **F6** — `agents/ingestion/wiring.py`: rename `weight_delta` ->
   `occurrence_weight` in the `SegmentWiringClient` Protocol, both
   `put_edge` implementations, both `execute_segment` call sites, and the
   docstring reference. Propagate the same rename into
   `agents/ingestion/test_segment_fixtures.py` and
   `agents/ingestion/test_segment_wiring.py` (fake client + call sites) so
   nothing breaks.

5. Run `pytest agents/ingestion/test_propose_split.py
   agents/ingestion/test_segment_wiring.py agents/ingestion/test_segment_fixtures.py`
   and `go test ./engine/rpc/...` (self-consistency, not verification).

6. One commit covering all four findings; update
   `.cdr/index/regression.jsonl` is NOT done here (verification agent's job
   per invariant separation — implementation does not mark findings
   resolved in the shared regression index).
