# Impact analysis -- issue #19, subtask 3.5.1

New files only -- no existing file modified:
- `data/load_bitext.py` (new)
- `data/load_enron.py` (new)
- `data/test_loaders.py` (new)
- `data/fixtures/bitext_sample.json` (new -- real 30-row sample of the public Bitext
  dataset, `CDLA-Sharing-1.0`, fetched via the Hugging Face `datasets-server` rows API)
- `data/fixtures/enron_sample/{msg_001.txt,msg_002_reply.txt,msg_003_no_optional_headers.txt}`
  (new -- format-faithful hand-authored fixtures; see `data/load_enron.py`'s module
  docstring for the disclosed reason a literal corpus download was not used)

No changes to `agents/ingestion/{segment,wiring,propose_split,shortlist}.py` or any
`engine/` Go file (out of scope, confirmed not needed for a loader-only task: both
loaders consume only `dispatch.py`'s existing public functions, unmodified).

Blast radius: purely additive. `data/` had no prior Python code, so there is zero risk
of regressing existing behavior in `data/`. `agents/ingestion/` is read-only from this
subtask's perspective (only imported, never edited), so the full `agents/` pytest
suite is an appropriate regression check (import errors would be the only way this
subtask could break existing agents/ tests, and none occurred).

Coupling introduced: `data/load_bitext.py`/`data/load_enron.py` import
`ingestion.dispatch` lazily (inside their `RawDocument`-building functions only), so
`data/` now has a soft runtime dependency on `agents/`'s installed package for its
`*_as_raw_documents`/`*_documents` entry points, but not for its lower-level
record-loading functions (`iter_bitext_records`, `bitext_row_to_ticket_json`,
`iter_enron_sample_paths`), which have zero dependency on `agents/`. This keeps
`data/` loadable/testable even in an environment without `agents/.venv` active,
degrading gracefully to `ModuleNotFoundError` only at the point where a `RawDocument`
is actually requested.
