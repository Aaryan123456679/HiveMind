"""PDF normalizer: converts a PDF file into plain text with page-boundary markers.

Uses `pymupdf` (imported as `fitz`) to extract per-page plain text and wraps each
page's text in an explicit open/close marker pair so downstream consumers (e.g. the
segmentation agent, or a future `RawDocument` builder in `agents/ingestion/dispatch.py`)
can reliably locate page boundaries and verify no page content was dropped.

Marker format (1-indexed page numbers)::

    [[PAGE 1]]
    <page 1 text>
    [[/PAGE 1]]
    [[PAGE 2]]
    <page 2 text>
    [[/PAGE 2]]

Every page in the source document -- including pages with no extractable text --
produces exactly one marker pair in the output, so the number of marker pairs always
equals the source document's page count.
"""

from __future__ import annotations

import re
from pathlib import Path

import fitz  # pymupdf

#: Matches a single ``[[PAGE n]] ... [[/PAGE n]]`` block and captures the page
#: number and the page's text. Public so downstream consumers can parse output
#: from :func:`normalize_pdf` without re-deriving the marker format.
PAGE_MARKER_RE = re.compile(
    r"\[\[PAGE (?P<page>\d+)\]\]\n(?P<text>.*?)\[\[/PAGE (?P=page)\]\]",
    re.DOTALL,
)


def _page_marker(page_number: int, text: str) -> str:
    """Wrap a single page's text in its open/close boundary markers."""
    if text and not text.endswith("\n"):
        text += "\n"
    return f"[[PAGE {page_number}]]\n{text}[[/PAGE {page_number}]]\n"


def normalize_pdf(path: str | Path) -> str:
    """Normalize a PDF file into plain text with per-page boundary markers.

    Args:
        path: Path to the PDF file to normalize.

    Returns:
        Plain text containing one ``[[PAGE n]] ... [[/PAGE n]]`` block per page in
        the source document, in page order (1-indexed), with no page dropped
        (including pages that contain no extractable text).

    Raises:
        Exception: Propagated from `pymupdf` if the file cannot be opened/parsed.
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
