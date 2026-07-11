# task-5.1.1: Common Bitext/Enron dataset-loader interface for agents/eval/ (issue #26)

## Summary

Issue #26 (milestone #7, Phase 5) requires `agents/eval/`'s benchmark
harness -- which compares three retrieval arms (HiveMind, vector-RAG
baseline, GraphRAG-style baseline) -- to see an identical corpus per arm,
per `docs/LLD/eval.md`'s "only the retrieval step varies between arms"
design. `agents/eval/` was scaffold-only (an empty `__init__.py`) and
task-3.5.1's already-shipped Bitext/Enron loaders (`data/load_bitext.py`,
`data/load_enron.py`) had no shared entry point into it. Subtask 5.1.1
closes that gap: `agents/eval/datasets.py` now exposes a single common
`load_dataset(name, *, limit=None)` / `available_datasets()` interface,
delegating straight through to the existing loaders with zero
reimplementation of loading or field-mapping logic.

## Features

- **`agents/eval/datasets.py`**: registry/dispatch module exposing
  `load_dataset(name, *, limit=None)` and `available_datasets()` as the
  one common interface all three future benchmark arms will use. Reuses
  `ingestion.rawdoc.RawDocument` (the existing source-type-agnostic record
  shape, issue #17/3.3.4) unchanged, and delegates via `yield from` to
  `data.load_bitext.load_bitext_as_raw_documents` /
  `data.load_enron.load_enron_documents` -- no field-mapping logic
  duplicated. A `_LOADERS` registry dict is the documented extension point
  for a future synthetic-PDF dataset.
- **`agents/eval/test_dataset_interface.py`**: 6 tests covering the test
  spec -- consistent `RawDocument` shape across both datasets (non-empty
  id, valid source_type, non-empty text, dict structured_fields,
  timestamp present), a distinct-source-types cross-dataset check, a
  limit-forwarding check, and an unknown-dataset-name `ValueError` check.

## Impact

- Only `agents/eval/datasets.py` and `agents/eval/test_dataset_interface.py`
  were added; `data/load_bitext.py`, `data/load_enron.py`, and
  `agents/ingestion/rawdoc.py` are untouched -- zero regression risk to
  task-3.5.1's already-verified loaders.
- `agents/.venv/bin/pytest eval/ -q`: 6 passed. Full `agents/` regression
  (`agents/.venv/bin/pytest . -q`): 348 passed, no new failures. `data/`
  regression (`agents/.venv/bin/pytest data/ -q`): 14 passed. `ruff check
  eval/`: clean.
- Unblocks later Phase 5 subtasks: the three benchmark-arm harnesses, and
  a future synthetic-PDF dataset registration into the same interface.
- Carried-forward, non-blocking technical debt (see Release Notes):
  production-code `sys.path` mutation copied from a test-only precedent,
  and an untested `limit=0` boundary case.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10001-verification`
- Commit: `4f6bc5f88f762b89172487f919a51f9c6d5a58f8`
- Verifier independently re-derived every claim from source rather than
  taking the implementer's word: confirmed `RawDocument`'s exact field set
  by reading `agents/ingestion/rawdoc.py` directly, confirmed both
  `data/load_bitext.py` and `data/load_enron.py` already yield
  `RawDocument` instances, confirmed the adapters use `yield from` (not a
  bare `yield`, which would have wrongly yielded the generator object
  itself), and independently re-ran all four test commands with matching
  pass counts (6 / 348 / 14, ruff clean). Zero blocking findings.

## Release Notes

- Added `agents/eval/datasets.py` and
  `agents/eval/test_dataset_interface.py`: a common
  `load_dataset`/`available_datasets` interface over the existing
  Bitext/Enron loaders, giving `agents/eval/`'s future benchmark arms a
  single, consistent entry point onto an identical corpus.
- **Non-blocking, carried-forward findings** (recorded in
  `.cdr/index/regression.jsonl`, both low severity, neither blocking):
  1. `agents/eval/datasets.py` performs cross-root `sys.path` mutation at
     import/call time (for `data/` <-> `agents/` imports), replicating a
     pattern previously used only in a test file
     (`agents/ingestion/test_e2e_smoke.py`, task-3.5.2). It is idempotent
     and `__file__`-anchored so not immediately fragile, but should be
     replaced by a proper packaging/pythonpath fix (e.g. an editable
     install of `data/`, or a repo-root `conftest.py`/`pytest.ini`
     `pythonpath` entry) before more modules copy the same pattern.
  2. No test exercises the `limit=0` boundary on `load_dataset`'s common
     interface. The underlying loaders' `index >= limit`-style comparisons
     should handle it correctly, but this is untested at either layer.
- This closes subtask 5.1.1 under issue #26 (milestone #7, Phase 5).
  Subtasks 5.1.2 and 5.1.3 are separate and not yet dispatched. This
  commit is pushed to `origin/main` as part of this same CDR-commit step
  (see below); GitHub issue/milestone state is not otherwise touched.
