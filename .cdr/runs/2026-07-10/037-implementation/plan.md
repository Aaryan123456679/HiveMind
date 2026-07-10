# Plan -- issue #19, subtask 3.5.1

1. Fetch a small real sample (30 rows) of the public Bitext customer-support dataset
   via the Hugging Face `datasets-server` rows API (network available in this
   session; verified license `CDLA-Sharing-1.0`); commit as
   `data/fixtures/bitext_sample.json` with provenance metadata.
2. Attempt to find a lightweight, genuinely-raw-headers Enron subsample via the same
   kind of API; none found with headers intact (all discoverable HF Enron-derived
   datasets were body-only). Disclosed decision: author 3 small format-faithful
   fixture message files under `data/fixtures/enron_sample/`, matching the exact
   on-disk shape `agents/ingestion/normalize_email.py` parses and the convention
   already established/verified by `agents/ingestion/testdata/enron_sample_*.txt`.
3. `data/load_bitext.py`: `iter_bitext_records` (raw rows) ->
   `bitext_row_to_ticket_json` (disclosed field mapping) -> `load_bitext_tickets`
   (normalizer-ready dicts) -> `load_bitext_as_raw_documents` (full `RawDocument`s via
   `dispatch_ticket_json`). Plus an explicit, test-independent
   `refresh_sample_via_datasets_server` maintenance utility.
4. `data/load_enron.py`: `iter_enron_sample_paths` (file discovery) ->
   `load_enron_documents` (full `RawDocument`s via `dispatch_email`).
5. `data/test_loaders.py`: exercise both loaders against their default local fixture,
   asserting record counts (30 / 3) and field presence, per the issue's test spec.
6. Run full `agents/` pytest suite + `ruff check` to confirm no regressions.
7. One local commit; no push, no GitHub-state changes.
