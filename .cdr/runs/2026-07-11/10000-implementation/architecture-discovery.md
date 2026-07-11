# Architecture discovery — task-5.1.1

## Index-first trail (per token order mandate)

- `.cdr/index/task.jsonl`: `task-3.5.1` (state=verified, commit 4883fc7) — "Bitext + Enron dataset
  loaders (issue #19, first of 2 subtasks)". `.cdr/commits/task-3.5.1.md` confirms the loaders
  live at `data/load_bitext.py` and `data/load_enron.py`, backed by `data/fixtures/bitext_sample.json`
  and `data/fixtures/enron_sample/`, and both build `ingestion.rawdoc.RawDocument` records via
  lazy `ingestion.dispatch` imports (`load_bitext_as_raw_documents`, `load_enron_documents`).
- `.cdr/index/file.jsonl` had no direct "bitext"/"enron" hits (index granularity is coarser than
  filenames here); confirmed via `git log`/filesystem search instead — `data/load_bitext.py`,
  `data/load_enron.py` exist and are tracked (task-3.5.1, task-3.5.2 follow-up in
  `agents/ingestion/test_e2e_smoke.py`).
- `.cdr/index/task.jsonl` also shows `issue-19` fully implemented locally (3.5.1 + 3.5.2), and no
  prior `task-5.1.*` entries — this is genuinely new work.

## Docs (HLD/LLD)

- `docs/HLD.md`: `agents/eval/` = "Benchmark harness against vector-RAG and GraphRAG baselines",
  see `docs/LLD/eval.md`.
- `docs/LLD/eval.md`: status "scaffold only (`agents/eval/__init__.py` empty)". Dataset loaders
  section lists Support tickets (Bitext), Enron email subsample, and synthetic PDFs (out of scope
  for 5.1.1). "All three arms share an identical final-answer LLM... so that only the retrieval
  step varies between arms" — implies the *dataset* feeding all three arms must also be identical,
  i.e. a single common interface, not three independent loader call sites.

## Existing code inspected (after docs/index)

- `agents/ingestion/rawdoc.py`: `RawDocument(id, source_type, text, structured_fields, timestamp)`
  — the *already-established* common record shape produced by every ingestion normalizer
  (issue #17, subtask 3.3.4). `source_type` in `{"pdf", "email", "ticket"}`.
- `data/load_bitext.py`: `load_bitext_as_raw_documents(path=DEFAULT_SAMPLE_PATH, *, limit=None)`
  → yields `RawDocument` (`source_type="ticket"`). Lazy-imports `ingestion.dispatch` only inside
  the function body.
- `data/load_enron.py`: `load_enron_documents(directory=DEFAULT_SAMPLE_DIR, *, limit=None)` →
  yields `RawDocument` (`source_type="email"`). Same lazy-import pattern.
- `agents/eval/__init__.py`: empty (scaffold only, per LLD doc).
- `agents/pyproject.toml`: `[tool.setuptools] packages = ["ingestion", "query", "llm", "eval"]`,
  installed editable (`agents/.venv`, `__editable__.hivemind_agents-0.1.0.pth`). Sibling packages
  import each other as bare top-level names, e.g. `agents/query/test_topic_selector_cap.py`:
  `from query.conftest import ...`, `from query.topic_selector import ...`. So `agents/eval/`
  modules should use `from eval.datasets import ...` / `from ingestion.rawdoc import ...`, not
  `agents.eval...`/`agents.ingestion...`.
- **Cross-boundary import precedent** (`data/` is a sibling of `agents/`, not inside it, and has
  no `__init__.py`): `agents/ingestion/test_e2e_smoke.py` (task-3.5.2) already does exactly the
  wiring 5.1.1 needs — `sys.path.insert(0, str(_REPO_ROOT / "agents"))` +
  `sys.path.insert(0, str(_DATA_DIR.parent))` then `from data.load_bitext import ...` /
  `from data.load_enron import ...` (relying on Python 3's implicit namespace packages for `data`,
  since it has no `__init__.py`). This is the established, working pattern to reuse rather than
  inventing a new cross-boundary import mechanism.
- `data/test_loaders.py`: pytest's rootdir-insertion import mode makes bare `import load_bitext`
  work when pytest is invoked over `data/` directly (unrelated to how `agents/eval/` needs to
  reach `data/`, since `agents/eval/` tests run under `agents/.venv`'s pytest against `agents/`,
  not `data/`).

## Conclusion for architecture

- Reuse `ingestion.rawdoc.RawDocument` as-is for the "common record shape" — it is already the
  system's canonical cross-source-type record type; inventing a parallel eval-only record type
  would violate "do not reimplement" in spirit and add unnecessary translation.
- `agents/eval/datasets.py` is a thin wiring/registry module: ensures `agents/` and repo-root are
  on `sys.path` (mirroring `test_e2e_smoke.py`'s precedent), then exposes
  `load_dataset(name, *, limit=None) -> Iterator[RawDocument]` plus `available_datasets()`,
  backed by a `{"bitext": ..., "enron": ...}` registry delegating to
  `data.load_bitext.load_bitext_as_raw_documents` / `data.load_enron.load_enron_documents`.
  This is the "common dataset-loader interface used by all three benchmark arms" the issue
  describes; the synthetic-PDF loader (issue #26's other stated dataset, out of scope for
  5.1.1) can register into the same dict in a later subtask without changing the interface shape.
