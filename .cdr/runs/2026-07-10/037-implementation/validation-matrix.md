# Validation matrix -- issue #19, subtask 3.5.1

| Acceptance criterion | Covered by |
|---|---|
| Bitext loader fetches/reads dataset from local/downloaded source | `data/fixtures/bitext_sample.json` (real downloaded sample) + `iter_bitext_records` reading it; `refresh_sample_via_datasets_server` for re-fetching |
| Enron loader fetches/reads dataset from local/downloaded source | `iter_enron_sample_paths` reads `data/fixtures/enron_sample/` (local); accepts any directory, incl. a real extracted corpus subtree |
| Both loaders yield RawDocument-ready inputs for the normalizers | `bitext_row_to_ticket_json`/`load_bitext_tickets` (normalize_ticket_json-ready dicts); `load_bitext_as_raw_documents`/`load_enron_documents` (full `RawDocument`s via `dispatch.py`) |
| Test spec: pytest `data/test_loaders.py` against small local fixture subset | `test_iter_bitext_records_count_and_fields`, `test_iter_enron_sample_paths_count`, etc. -- all run against `DEFAULT_SAMPLE_PATH`/`DEFAULT_SAMPLE_DIR`, no network |
| Assert expected record counts | `test_iter_bitext_records_count_and_fields` (30), `test_load_bitext_as_raw_documents_full_sample` (30), `test_iter_enron_sample_paths_count` (3), `test_load_enron_documents_count_and_shape` (3) |
| Assert field presence | `test_bitext_row_to_ticket_json_shape` (exact key set), `test_load_bitext_as_raw_documents_shape` (RawDocument fields), `test_load_enron_documents_count_and_shape` (sender/subject/thread present) |
| No regression to existing `agents/ingestion/` normalizers/tests | Full `agents/` pytest suite: 155 passed, 0 failed |
| No new lint findings | `ruff check data/ agents/`: 0 findings in `data/`; 1 pre-existing finding in generated `agents/hivemind_pb2_grpc.py` (unrelated, unowned) |
