# Plan — issue #53 subtask 4.5.15.5

1. `agents/ingestion/normalize_pdf.py`:
   - Add a `NormalizedPdfText(str)` subclass (with `__new__(cls, text, page_count)`) directly
     above `normalize_pdf`, documenting that it behaves exactly like `str` but also carries
     `page_count` as a free attribute, populated without any extra pass over the text.
   - Change `normalize_pdf`'s return type annotation to `NormalizedPdfText` and its final
     statement to `return NormalizedPdfText("".join(blocks), page_count=len(blocks))`.
   - Update `normalize_pdf`'s docstring `Returns:` section to mention `page_count`.
2. `agents/ingestion/dispatch.py`:
   - In `dispatch_pdf`: replace `page_count = sum(1 for _ in iter_pages(text))` with
     `page_count = text.page_count`.
   - Remove `iter_pages` from the `from ingestion.normalize_pdf import iter_pages,
     normalize_pdf` import line (now unused).
3. `agents/ingestion/test_dispatch.py`:
   - Add `test_dispatch_pdf_page_count_matches_normalize_pdf_no_second_parse`: build a PDF
     fixture, monkeypatch `ingestion.normalize_pdf.iter_pages` to raise `AssertionError` if
     called, call `dispatch_pdf`, and assert `doc.structured_fields["page_count"] ==
     len(PDF_PAGE_TEXTS)`. This proves both correctness and that no second `iter_pages` parse
     happens.
4. Run `pytest agents/ingestion/ -v` (full suite, not just `-k page_count`) to confirm no
   regression in `test_normalize_pdf.py` or elsewhere, then the exact test-spec command
   `pytest agents/ingestion/test_dispatch.py -k page_count`.
5. Run `ruff check agents/ingestion/` (repo's existing lint convention per prior subtask
   commit messages) to confirm the unused-import removal is clean and no new lint issues.
6. Self-consistency check, one local commit (no push), handoff.json.
