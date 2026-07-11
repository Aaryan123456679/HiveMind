# Requirement — Issue #53, subtask 4.5.15.5

Source: `gh issue view 53` (milestone #10, Phase 4.5 storage-engine/ingestion technical-debt
follow-ups), subtask 5 of 6.

## Subtask text (verbatim from GitHub issue #53)

**4.5.15.5 — Have normalize_pdf return page_count directly instead of dispatch_pdf
recomputing it via a second pass**

- Acceptance criteria: `normalize_pdf` returns `page_count` as part of its result (or a
  lightweight accompanying value), and `dispatch.py`'s `dispatch_pdf` uses it directly
  instead of `sum(1 for _ in iter_pages(text))`'s redundant second O(n) pass.
- Test spec: `pytest agents/ingestion/test_dispatch.py -k page_count`: assert
  `dispatch_pdf`'s reported `page_count` matches `normalize_pdf`'s own count with no
  second parse.
- Impacted modules: `agents/ingestion/normalize_pdf.py`, `agents/ingestion/dispatch.py`,
  `agents/ingestion/test_dispatch.py`.

Note: `agents/ingestion/test_normalize_pdf.py` is NOT in the impacted-modules list, which
constrains the design: whatever change is made to `normalize_pdf`'s return value must not
require touching that file's existing assertions (`normalize_pdf(...)` is currently
consumed as a plain `str` by `PAGE_MARKER_RE.finditer(result)`, `iter_pages(result)`, and
direct string operations in ~8 existing tests).

## Origin / provenance

- Finding first recorded during issue #17 subtask 3.3.4 verification
  (`.cdr/runs/2026-07-09/028-verification`, regression.jsonl): "dispatch_pdf recomputes
  page_count via a redundant second iter_pages() pass over already-parsed marker text
  instead of normalize_pdf returning count directly; negligible perf cost, non-blocking."
- Deferred to milestone #10 / issue #53 as subtask 4.5.15.5.
- Prior sibling subtasks in this same issue that also touch `normalize_pdf.py`:
  - 4.5.15.1 (commit `974926d`) — docstring-only "Trust boundary" additions to the module
    docstring and `iter_pages()`'s docstring, plus 3 new tests in
    `test_normalize_pdf.py` (`-k len_trust`). No parsing/production-logic change.
  - 4.5.15.2 (commit `721077b`) — test-only Unicode round-trip regression tests added to
    `test_normalize_pdf.py`. Explicitly "No production code changed" per its own commit
    message.
  - Both confirmed via `git show <hash> --stat` before starting this subtask; neither
    touches `dispatch.py` or the production logic this subtask changes, so there is no
    overlap/conflict risk with 4.5.15.5's planned edit.

## Acceptance criteria (restated, checkable)

1. `normalize_pdf(path)` exposes the page count it already derived while building the
   marker text (via `fitz`'s per-page iteration), with **zero extra full-document or
   full-text pass** to compute it.
2. `dispatch_pdf` in `dispatch.py` uses that exposed page count directly and no longer
   calls `sum(1 for _ in iter_pages(text))` (a redundant second O(n) parse of the marker
   text it just produced).
3. `dispatch_pdf`'s reported `structured_fields["page_count"]` still matches the true
   page count (parity with current behavior — no regression).
4. A new/updated test in `agents/ingestion/test_dispatch.py`, matched by
   `pytest agents/ingestion/test_dispatch.py -k page_count`, asserts (a) the reported
   `page_count` is correct, and (b) `iter_pages` is not invoked by `dispatch_pdf` (proves
   the redundant second pass is gone, not just that the answer happens to still be right).
5. No existing test in `agents/ingestion/test_normalize_pdf.py` or elsewhere is broken by
   the change to `normalize_pdf`'s return value (it must remain fully `str`-compatible:
   equality, regex matching via `PAGE_MARKER_RE.finditer`, `iter_pages(...)` parsing,
   string concatenation, etc.).
