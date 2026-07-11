# Plan — task-5.1.1

1. **`agents/eval/datasets.py`** (new)
   - Module docstring disclosing: reuses `ingestion.rawdoc.RawDocument` as the common record
     shape (not a new type); reuses `data/load_bitext.py` + `data/load_enron.py` unchanged
     (task-3.5.1); replicates `agents/ingestion/test_e2e_smoke.py`'s `sys.path` wiring pattern
     for the `agents/` <-> `data/` cross-root boundary.
   - `_ensure_cross_root_imports()` helper: inserts repo-root `agents/` dir and repo root itself
     onto `sys.path` (idempotent, checks membership first) so `ingestion.rawdoc` and
     `data.load_bitext`/`data.load_enron` are both importable regardless of which directory
     pytest's rootdir-insertion added.
   - `available_datasets() -> tuple[str, ...]`: registered dataset names, currently
     `("bitext", "enron")`.
   - `load_dataset(name: str, *, limit: int | None = None) -> Iterator[RawDocument]`: looks up
     `name` in an internal registry dict of zero-arg-except-limit callables, raises
     `ValueError` with the available-name list on unknown `name` (fail fast, clear error, no
     silent empty iterator).
   - Two private per-dataset adapter functions (`_load_bitext`, `_load_enron`) that lazily import
     `data.load_bitext`/`data.load_enron` inside the function body (mirrors those modules' own
     lazy-import-of-`ingestion` discipline, so importing `agents/eval/datasets.py` itself never
     requires `data/`'s fixtures to be reachable before the module is actually used) and delegate
     straight through to `load_bitext_as_raw_documents`/`load_enron_documents` with `limit`
     forwarded, no field-mapping/translation logic of their own.

2. **`agents/eval/test_dataset_interface.py`** (new)
   - Per test spec: load both datasets through the common interface, assert consistent record
     shape.
   - `test_available_datasets_includes_bitext_and_enron`: sanity check on the registry.
   - `test_load_dataset_yields_consistent_record_shape` (parametrized over
     `available_datasets()`): for each dataset, `load_dataset(name, limit=5)`, assert non-empty,
     assert every yielded record is a `RawDocument` instance with non-empty string `id`,
     `source_type` in the three valid literals, non-empty string `text`, dict
     `structured_fields`, and non-None `timestamp` -- i.e. genuinely checks *shape consistency*
     across the two different underlying source types, not just "didn't crash".
   - `test_bitext_and_enron_have_distinct_source_types`: cross-dataset assertion that Bitext
     yields `"ticket"` and Enron yields `"email"` -- guards against a future registry mix-up
     silently making both datasets look identical.
   - `test_unknown_dataset_name_raises_value_error`: `load_dataset("nope")` raises `ValueError`.

3. No changes to any other file. Run `agents/.venv/bin/pytest agents/eval/ data/ agents/ingestion/ -q`
   and `agents/.venv/bin/ruff check agents/eval/` as the self-consistency gate (build green).

4. One local commit, `feat:` type, Problem/Solution/Impact body matching repo convention (see
   `.cdr/commits/task-3.5.1.md` / `git log`), no push.
