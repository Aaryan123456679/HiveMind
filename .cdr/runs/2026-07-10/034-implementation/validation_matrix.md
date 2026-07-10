# Validation matrix

| # | Requirement | Covered by | Result |
|---|---|---|---|
| 1 | Fixture-doc suite verifies structured output shape across representative doc types | `test_segment_fixtures.py::test_segment_returns_expected_shape_across_doc_types` (parametrized: notes/`pdf`-stand-in, `email`, `ticket`) | pass |
| 2 | Fixture suite covers whole small multi-topic corpus, not one doc | `test_segment_fixtures.py::test_segment_across_notes_corpus_documents` | pass |
| 3 | End-to-end pipeline composition (shortlist->segment->wiring), mocked | `test_segment_fixtures.py::test_pipeline_shortlist_segment_wiring_end_to_end` | pass |
| 4 | Optional live-Ollama smoke test, skip-if-unavailable | `test_segment_live.py` (`pytestmark` skipif); ran for real in this environment (Ollama reachable, llama3.1:8b) | pass (not skipped here; skip path verified logically -- `_ollama_is_reachable()` returns False on any `httpx.HTTPError`) |
| 5 | F1 resolved: `segment.py` tolerates markdown-fenced JSON | `test_segment.py::test_markdown_code_fence_wrapped_json_is_parsed`, `test_plain_code_fence_wrapped_json_is_parsed`; also exercised live against a real model in `test_segment_live.py` | pass |
| 6 | `propose_split.py` behavior unchanged after extraction | `test_propose_split.py` full file (incl. its own pre-existing fence test) | pass |
| 7 | No regressions across `agents/` | Full `pytest agents/` run | 153 passed, 2 pre-existing failures (protobuf gencode/runtime mismatch in `test_shortlist.py`'s two gRPC-translation tests) confirmed present on base commit `656b612` via `git stash` before/after comparison -- unrelated to this change |
| 8 | ruff clean | `ruff check agents/` | 0 new findings; 1 pre-existing finding in generated `hivemind_pb2_grpc.py` (unowned generated file, unrelated) |
