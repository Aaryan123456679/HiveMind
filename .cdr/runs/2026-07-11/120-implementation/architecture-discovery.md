# Architecture discovery — 4.5.15.2 (built fresh, no prior context)

## Files read (fresh, this run)
- `agents/ingestion/normalize_pdf.py` (162 lines, HEAD=bdbbbf7, unchanged
  since 974926d / 4.5.15.1) — production module, NOT modified by this subtask.
- `agents/ingestion/test_normalize_pdf.py` (200 lines) — test module, the
  ONLY file this subtask touches.

## Key facts relevant to the unicode round-trip test

1. `_page_marker(page_number, text)` (normalize_pdf.py:64-72):
   - Appends `"\n"` to `text` if it doesn't already end with one.
   - Computes `LEN=len(text)` — Python's `len()` on a `str` counts *Unicode
     code points*, not UTF-16 code units or UTF-8 bytes. This matters for
     the test design: Python strings are sequences of code points, so an
     astral-plane emoji (e.g. U+1F600, outside the BMP) counts as exactly 1
     toward `len()`, same as it would when slicing `normalized_text[start:end]`
     later in `iter_pages`. There is no UTF-16 surrogate-pair-splitting risk
     in Python (unlike JS), so a naive "1 emoji = 1 char" test assumption is
     actually correct here.
   - Returns `f"[[PAGE {n} LEN={len(text)}]]\n{text}[[/PAGE {n}]]\n"`.

2. `iter_pages(normalized_text)` (normalize_pdf.py:102-162):
   - `PAGE_MARKER_RE = re.compile(r"\[\[PAGE (?P<page>\d+) LEN=(?P<len>\d+)\]\]\n")`
     — matches ASCII digits only for page/len; irrelevant to payload content,
     which can be arbitrary Unicode.
   - Slices `normalized_text[payload_start:payload_end]` using the `LEN`
     value directly as a code-point-count offset — consistent with how
     `_page_marker` computed `LEN`, so round-trip is exact for any Unicode
     content as long as the string is not re-encoded/decoded (e.g. to
     UTF-8 bytes) anywhere in between. No such re-encoding happens in this
     module — it operates purely on Python `str` throughout.
   - After slicing, requires the very next bytes to match `_PAGE_CLOSE_RE`
     (`\[\[/PAGE (?P<page>\d+)\]\]\n`) — again ASCII-only marker syntax,
     unaffected by Unicode payload content.

3. Existing test file conventions (test_normalize_pdf.py):
   - Uses `_page_marker` + `iter_pages` directly (bypassing the PDF/fitz
     layer) for marker-format-level tests not requiring a real PDF fixture
     — see `test_page_text_containing_its_own_close_marker_survives_round_trip`
     (lines 92-116) and the F3 trust-boundary tests (lines 162-201). This is
     the established pattern to follow for a pure marker-format unicode
     round-trip test (per the issue's test spec:
     `iter_pages(_page_marker(1, unicode_text))`), no PDF fixture needed.
   - Tests are grouped under `# --- <label> ---` section-comment headers.
   - Imports already include `_page_marker` and `iter_pages` from
     `ingestion.normalize_pdf` (line 14-19) — no new imports needed for a
     pure-Python-string round-trip test.

## Conclusion
No production code changes required or in scope. This subtask is a pure
test-file addition: one new test function (or a small parametrized set)
appended to `test_normalize_pdf.py` in a new trailing section, using
`_page_marker` + `iter_pages` directly on a hand-built Unicode string,
matched by `-k unicode_round_trip`.
