# Plan — normalize_pdf.py

## Page-marker format
Chose explicit, greppable, 1-indexed markers wrapping each page's text:

```
[[PAGE 1]]
<page 1 text>
[[/PAGE 1]]
[[PAGE 2]]
<page 2 text>
[[/PAGE 2]]
```

Rationale:
- Open+close markers per page (not just a boundary line) make it unambiguous where a
  page's content starts and ends even if the page text itself contains blank lines or
  text resembling a marker-like line, and make "no content dropped between markers"
  mechanically checkable in tests via regex extraction per page.
- `[[PAGE n]]` uses double brackets and uppercase keyword to be extremely unlikely to
  collide with real PDF body text, easy to `re.finditer` on, and readable.
- 1-indexed page numbers match how humans/PDF viewers refer to pages (page 1, not page
  0), reducing downstream confusion when this feeds later pipeline stages (segmentation
  agent, RawDocument).
- Documented in the module docstring and a `PAGE_MARKER_RE` regex constant so subtask
  3.3.4 (dispatch/RawDocument) or any other consumer can rely on a stable, importable
  parsing contract rather than re-deriving the format.

## Public API
```python
def normalize_pdf(path: str | Path) -> str:
    """Read a PDF file and return plain text with per-page boundary markers."""
```
- Accepts a path (str or pathlib.Path) to a PDF file.
- Opens via `fitz.open(path)` (pymupdf), iterates `doc.page_count` pages in order,
  extracts each page's text via `page.get_text()` ("text" mode -- plain text, plain
  reading order), wraps each page's text in the markers above, joins with newlines.
- Guarantees every page in the source doc produces a marker pair in the output, even if
  a page's extracted text is empty (empty pages must not be silently dropped -- this is
  central to the "no page content dropped" acceptance criterion, interpreted as "no page
  dropped").
- Raises on missing/invalid file rather than swallowing errors (let pymupdf's own
  exception propagate; no bespoke error handling needed for this scope).

## Fixture PDF
Built in a pytest fixture (`tmp_path`-based) using `fitz` itself:
- Create a new `fitz.open()` document, add 3 pages with distinct, easily-assertable text
  content per page (e.g. "Page one content." / "Page two content." / "Page three
  content."), save to a temp file, yield the path.
- Avoids committing a binary fixture file and avoids adding a new dependency
  (`reportlab`), consistent with pymupdf already being a hard dependency and no existing
  binary-fixture convention in the repo.

## Test cases (see validation-matrix.json for full mapping)
1. Marker presence: all 3 `[[PAGE n]]`...`[[/PAGE n]]` pairs present, in order 1..3.
2. Content preservation: each page's known text appears within its own marker pair (not
   bled into an adjacent page).
3. No dropped pages: number of marker pairs in output == `doc.page_count` of the source
   fixture.
4. Empty-page edge case: a fixture with a blank page still yields a marker pair for that
   page (not skipped).

## Out of scope
- RawDocument wrapping / dispatch (3.3.4).
- Email/ticket normalizers (3.3.2/3.3.3).
- OCR / scanned-PDF text extraction fallback (not required by acceptance criteria; pure
  pymupdf `get_text()` is sufficient for text-layer PDFs which is what a
  pymupdf-generated fixture produces).
