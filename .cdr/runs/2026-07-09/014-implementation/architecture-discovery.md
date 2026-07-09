# Architecture Discovery

## Index consulted (`.cdr/index/`)
- `file.jsonl`: only entry for `agents/ingestion` module is the LLD doc itself
  (`docs/LLD/ingestion-agent.md`, last_change_run `2026-07-03-001-documentation`).
  No prior implementation runs recorded against `agents/ingestion/*.py`. Confirms this
  is greenfield work — scaffold-only package (`agents/ingestion/__init__.py` is empty).
- `task.jsonl` / `regression.jsonl`: no PDF/normalize-related entries found.

## HLD / LLD
- `docs/HLD.md` — system context (not re-read in full; ingestion-agent LLD is the
  targeted doc per issue scope).
- `docs/LLD/ingestion-agent.md` — status "scaffold only". Key points:
  - Per-doc-type normalization: "PDF: via `pymupdf` -> plain text + page markers."
    (matches issue text verbatim — LLD and issue agree, no discrepancy).
  - All normalizers ultimately produce a common `RawDocument{id, sourceType, text,
    structuredFields, timestamp}` record — but that record type/dispatch is subtask
    3.3.4 (`rawdoc.py`, `dispatch.py`), explicitly out of scope here. This subtask only
    needs the PDF-specific normalize function returning marked-up text.
  - No specific page-marker format is mandated by either issue or LLD — left to
    implementer. Chose to design and document the format (see plan.md).

## Existing code / conventions (read after indexes, per token order)
- `agents/pyproject.toml`: single Python package `hivemind-agents`, subpackages
  `ingestion`, `query`, `llm`, `eval`. Deps already include `pymupdf>=1.24` (no change
  needed to dependency declarations). Dev deps: `pytest>=8.3`, `ruff>=0.6`.
  `[tool.pytest.ini_options] testpaths = ["."]` — pytest run from `agents/` (or
  `pytest agents/ingestion/test_normalize_pdf.py` from repo root both work since
  testpaths is relative to invocation dir when run from `agents/`; issue's exact
  command `pytest agents/ingestion/test_normalize_pdf.py` is run from repo root using
  pytest's file-path collection which works regardless of testpaths).
- `agents/.venv`: pymupdf 1.28.0 already installed in the project venv — no install step
  needed to run tests locally.
- `agents/ingestion/`, `agents/llm/`, `agents/query/`, `agents/eval/`: all currently
  contain only an empty `__init__.py`. No pre-existing normalizer, test, or fixture code
  to pattern-match against — this is the first real implementation in `agents/`.
  No `fixtures/`/`testdata/` directory exists yet anywhere under `agents/`.
- No other Python source in the repo outside `agents/` (rest of repo is Go, per prior
  session context on the Go engine).

## Conventions decided (absent prior precedent)
- Standard library `pytest` test style (function-based, `assert` statements) — matches
  `pytest` dev dependency already declared; no other test framework present.
  - Type hints on public functions/dataclasses (matches `pydantic>=2.9` dependency
  present in the project, indicating a typed-Python style), module-level docstring,
  Google/plain docstring on the public function.
- Fixture PDF generated programmatically at test time via `pymupdf` itself (already a
  hard dependency, no new dependency needed) rather than committing a binary PDF fixture
  or adding `reportlab`. This keeps the test hermetic and avoids adding a new dependency
  or a binary blob to the repo, consistent with there being no existing binary-fixture
  convention in this codebase.
