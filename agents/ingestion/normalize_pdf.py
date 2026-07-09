"""PDF normalizer: converts a PDF file to plain text with page-boundary markers.

Uses `pymupdf` (imported as `fitz`) to extract per-page plain text and wraps
each page's text in an explicit, length-prefixed marker pair so downstream
consumers (e.g. the segmentation agent, future `RawDocument` builder in
`agents/ingestion/dispatch.py`) can reliably locate page boundaries and
verify no page content was dropped.

Marker format (1-indexed page numbers; ``LEN=<n>`` is the exact character
count of the page's text payload, INCLUDING the trailing newline the
normalizer always appends)::

    [[PAGE 1 LEN=19]]
    <page 1 text>
    [[/PAGE 1]]
    [[PAGE 2 LEN=19]]
    <page 2 text>
    [[/PAGE 2]]

The explicit ``LEN=<n>`` header lets a parser slice exactly `n` characters as
the page's payload instead of scanning the payload for a closing marker.
This makes the format immune to a whole class of bugs where a page's own
extracted text happens to contain a literal substring that looks like a page
marker (e.g. a PDF whose content documents this very marker syntax) -- such
text is carried through byte-for-byte without truncation, escaping, or any
other content transformation.

Every page in the source document -- including pages with no extractable
text -- produces exactly one marker pair in the output, so the number of
marker pairs always equals the source document's page count.

Use :func:`iter_pages` to parse the output of :func:`normalize_pdf` back into
``(page_number, text)`` pairs. Do not parse the format with a naive
"non-greedy regex to the closing marker" approach: that is exactly the
ambiguity this length-prefixed format is designed to avoid.
"""

from __future__ import annotations

import re
from collections.abc import Iterator
from pathlib import Path

import fitz  # pymupdf

#: Matches a single ``[[PAGE n LEN=k]]`` opening marker header line and
#: captures the page number and payload length. Public so downstream
#: consumers can locate page boundaries without re-deriving the marker
#: format. Does NOT match page content -- a page's text may itself contain
#: marker-lookalike substrings, which no content-matching regex can safely
#: disambiguate. Use :func:`iter_pages` to safely extract a page's text.
PAGE_MARKER_RE = re.compile(r"\[\[PAGE (?P<page>\d+) LEN=(?P<len>\d+)\]\]\n")

#: Matches a page's closing marker line.
_PAGE_CLOSE_RE = re.compile(r"\[\[/PAGE (?P<page>\d+)\]\]\n")


def _page_marker(page_number: int, text: str) -> str:
    """Wrap a single page's text in its open/close boundary markers."""
    if text and not text.endswith("\n"):
        text += "\n"
    return (
        f"[[PAGE {page_number} LEN={len(text)}]]\n"
        f"{text}"
        f"[[/PAGE {page_number}]]\n"
    )


def normalize_pdf(path: str | Path) -> str:
    """Normalize a PDF file to plain text with per-page boundary markers.

    Args:
        path: Path to the PDF file to normalize.

    Returns:
        Plain text containing one ``[[PAGE n LEN=k]] ... [[/PAGE n]]`` block
        per page (1-indexed), in page order. See the module docstring for
        the exact marker format, and :func:`iter_pages` for parsing it back
        without risk of content-boundary ambiguity.

    Raises:
        Exception: Propagated from `pymupdf` if the file cannot be opened or
            parsed (e.g. missing path, corrupt/non-PDF file).
    """
    doc = fitz.open(str(path))
    try:
        blocks = [
            _page_marker(page_index + 1, page.get_text())
            for page_index, page in enumerate(doc)
        ]
    finally:
        doc.close()
    return "".join(blocks)


def iter_pages(normalized_text: str) -> Iterator[tuple[int, str]]:
    """Parse :func:`normalize_pdf` output back into ``(page_number, text)`` pairs.

    Uses each page's ``LEN=<n>`` header to slice exactly `n` characters of
    payload, so a page's own text may safely contain marker-lookalike
    substrings (e.g. a literal ``[[/PAGE 1]]``) without being truncated or
    otherwise corrupting parsing -- this is the safe way to consume
    :func:`normalize_pdf` output; do not re-derive a content-scanning regex.

    Args:
        normalized_text: Output of :func:`normalize_pdf`.

    Yields:
        ``(page_number, text)`` tuples in page order.

    Raises:
        ValueError: If a page header is malformed, the payload length does
            not fit in the remaining text, or the expected closing marker is
            missing or mismatched.
    """
    pos = 0
    length = len(normalized_text)
    while pos < length:
        header_match = PAGE_MARKER_RE.match(normalized_text, pos)
        if header_match is None:
            raise ValueError(f"Malformed page marker header at offset {pos}.")
        page_number = int(header_match.group("page"))
        payload_len = int(header_match.group("len"))
        payload_start = header_match.end()
        payload_end = payload_start + payload_len
        if payload_end > length:
            raise ValueError(
                f"Page {page_number} LEN={payload_len} exceeds remaining text."
            )
        text = normalized_text[payload_start:payload_end]
        close_match = _PAGE_CLOSE_RE.match(normalized_text, payload_end)
        if close_match is None or int(close_match.group("page")) != page_number:
            raise ValueError(
                f"Missing or mismatched closing marker for page {page_number}."
            )
        yield page_number, text
        pos = close_match.end()
