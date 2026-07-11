# Architecture discovery — issue #53 subtask 4.5.15.5

## Token order followed
1. `.cdr/index/*.jsonl` (feature-index, regression.jsonl, pending.md-derived entries) — read
   first; found the origin finding (issue #17 3.3.4 verification, 028-verification) and prior
   sibling-subtask commits (974926d for 4.5.15.1, 721077b for 4.5.15.2).
2. `docs/LLD/ingestion-agent.md` — high-level only ("PDF via pymupdf -> plain text with page
   markers"); no low-level marker-format detail lives here, so no LLD update is needed for this
   subtask (no design-level contract changes, just an internal perf/coupling fix).
3. Touched-file read: `agents/ingestion/normalize_pdf.py` (full), `agents/ingestion/dispatch.py`
   (full), `agents/ingestion/test_dispatch.py` (relevant sections).
4. `git show 974926d --stat` and `git show 721077b --stat` to confirm no overlap with this
   subtask's planned edits (both confirmed prior/doc/test-only, no touch to `dispatch.py` or
   `normalize_pdf`'s production return-value contract).

## Current state confirmed

- `normalize_pdf.py`: `normalize_pdf(path) -> str` builds `blocks = [_page_marker(i+1,
  page.get_text()) for i, page in enumerate(doc)]` then returns `"".join(blocks)`. The page
  count (`len(blocks)`) is already known for free at this point — no extra fitz call or extra
  pass needed to obtain it. 4.5.15.1's "Trust boundary" docstring paragraphs (module + iter_pages)
  are present and unrelated to this change. 4.5.15.2's Unicode round-trip tests are test-only
  additions to `test_normalize_pdf.py`, no production code touched.
- `dispatch.py`: `dispatch_pdf` calls `text = normalize_pdf(path)` then
  `page_count = sum(1 for _ in iter_pages(text))` — a full second regex-driven parse of the
  marker text `normalize_pdf` just built, purely to count marker pairs it already knows the
  count of.
- `test_dispatch.py`: existing `test_dispatch_pdf_structured_fields_page_count` already checks
  the *value* is correct but does not prove the redundant second pass is gone.
- `test_normalize_pdf.py`: ~8 existing tests consume `normalize_pdf(...)`'s return value as a
  plain `str` (regex `.finditer`, `iter_pages(result)`, direct string slicing/equality). This
  file is NOT in the subtask's impacted-modules list, so the return-value change must remain
  100% `str`-compatible for these call sites without modification.

## Design decision

Introduce a `str` subclass, `NormalizedPdfText`, with an additional `page_count: int`
attribute set at construction time from the already-known `len(blocks)`. `normalize_pdf`
returns an instance of this subclass instead of a plain `str`.

- Fully backward compatible: a `str` subclass instance IS a `str` for every purpose exercised
  by existing tests (equality, `+`/`.join`, regex `.finditer`/`.match`, `iter_pages(...)`
  parsing, `len()`, slicing). No behavior change for any existing caller/test.
- Satisfies the acceptance criterion literally: "`normalize_pdf` returns `page_count` as part
  of its result (or a lightweight accompanying value)" — `page_count` is a free attribute on
  the very object `normalize_pdf` returns, not a second value that has to be unpacked (which
  would break every existing `str`-shaped call site including ones outside this subtask's
  scope).
- `dispatch_pdf` reads `text.page_count` directly: zero extra parsing, and the `iter_pages`
  import becomes unused in `dispatch.py` and is removed (ruff/pyflakes would flag the unused
  import otherwise).

## Impact analysis summary (see impact-analysis.json for full detail)

- `agents/ingestion/normalize_pdf.py`: add `NormalizedPdfText` class; change `normalize_pdf`'s
  return type annotation and final `return` statement. No change to `_page_marker`,
  `iter_pages`, `PAGE_MARKER_RE`, `_PAGE_CLOSE_RE`, or any docstring content added by 4.5.15.1.
- `agents/ingestion/dispatch.py`: `dispatch_pdf` reads `.page_count` off the returned object
  instead of calling `iter_pages`; drop the now-unused `iter_pages` import.
- `agents/ingestion/test_dispatch.py`: add one new test (matched by `-k page_count`) asserting
  both correctness and "no second parse" (via monkeypatching `iter_pages` in
  `ingestion.normalize_pdf` to raise if called, then confirming `dispatch_pdf` still succeeds
  with the correct count).
- No change needed to `test_normalize_pdf.py`, `rawdoc.py`, or any other consumer — confirmed
  by `grep -rn "normalize_pdf(" --include="*.py"` returning only the four sites above.
