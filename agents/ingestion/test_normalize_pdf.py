"""Tests for `agents.ingestion.normalize_pdf`.

Uses a small multi-page PDF fixture generated at test time via `pymupdf` itself
(no committed binary fixture, no new test-only dependency).
"""

from __future__ import annotations

from pathlib import Path

import fitz
import pytest

from ingestion.normalize_pdf import (
    PAGE_MARKER_RE,
    _page_marker,
    iter_pages,
    normalize_pdf,
)

PAGE_TEXTS = [
    "Page one content.",
    "Page two content.",
    "Page three content.",
]


def _make_pdf(tmp_path: Path, page_texts: list[str]) -> Path:
    """Build a PDF with one page per string in `page_texts` (may be empty strings)."""
    doc = fitz.open()
    try:
        for text in page_texts:
            page = doc.new_page()
            if text:
                page.insert_text((72, 72), text)
    finally:
        pdf_path = tmp_path / "fixture.pdf"
        doc.save(str(pdf_path))
        doc.close()
    return pdf_path


@pytest.fixture
def fixture_pdf(tmp_path: Path) -> Path:
    return _make_pdf(tmp_path, PAGE_TEXTS)


def test_page_markers_present_in_order(fixture_pdf: Path) -> None:
    result = normalize_pdf(fixture_pdf)
    matches = list(PAGE_MARKER_RE.finditer(result))
    page_numbers = [int(m.group("page")) for m in matches]
    assert page_numbers == [1, 2, 3]


def test_page_content_preserved(fixture_pdf: Path) -> None:
    result = normalize_pdf(fixture_pdf)
    pages = list(iter_pages(result))
    assert len(pages) == len(PAGE_TEXTS)
    for (page_number, text), expected_text in zip(pages, PAGE_TEXTS):
        assert expected_text in text
    # Ensure content did not bleed across page boundaries.
    page1_text = pages[0][1]
    assert "Page two" not in page1_text
    assert "Page three" not in page1_text


def test_marker_count_matches_page_count(fixture_pdf: Path) -> None:
    doc = fitz.open(str(fixture_pdf))
    try:
        expected_page_count = doc.page_count
    finally:
        doc.close()
    result = normalize_pdf(fixture_pdf)
    matches = list(PAGE_MARKER_RE.finditer(result))
    assert len(matches) == expected_page_count


def test_empty_page_still_gets_marker(tmp_path: Path) -> None:
    page_texts = ["First page has text.", "", "Third page has text."]
    pdf_path = _make_pdf(tmp_path, page_texts)
    result = normalize_pdf(pdf_path)
    matches = list(PAGE_MARKER_RE.finditer(result))
    page_numbers = [int(m.group("page")) for m in matches]
    assert page_numbers == [1, 2, 3]
    pages = dict(iter_pages(result))
    assert pages[2].strip() == ""


# --- F1 regression: marker-lookalike text embedded in a page's own content ---


def test_page_text_containing_its_own_close_marker_survives_round_trip() -> None:
    """Regression test for issue #17 3.3.1 verification finding F1.

    A page's extracted text may legitimately contain a literal substring
    that looks like its own closing marker (e.g. a PDF/spec document that
    discusses this marker syntax). Content-scanning parsers historically
    truncated the page at that point while marker *count* stayed correct,
    silently dropping real content. `iter_pages` must return the full,
    untruncated text.
    """
    page1_text = (
        "This spec discusses the marker syntax [[/PAGE 1]] used for "
        "boundaries.\nMore real content after it.\n"
    )
    page2_text = "Page two content.\n"
    blob = _page_marker(1, page1_text) + _page_marker(2, page2_text)

    pages = list(iter_pages(blob))

    assert len(pages) == 2
    assert pages[0] == (1, page1_text)
    assert pages[1] == (2, page2_text)
    # The content that a truncating parser would have dropped must be present.
    assert "More real content after it." in pages[0][1]


def test_page_text_containing_other_pages_marker_lookalike_survives() -> None:
    """A page's text containing a *different* page's marker text must also
    survive intact (guards against the general delimiter-in-payload class,
    not just the self-referential case)."""
    page1_text = "Refers to [[PAGE 2 LEN=999]] and [[/PAGE 2]] as examples.\n"
    page2_text = "Real page two content.\n"
    blob = _page_marker(1, page1_text) + _page_marker(2, page2_text)

    pages = list(iter_pages(blob))

    assert pages == [(1, page1_text), (2, page2_text)]


# --- F2: cheap additional coverage ---


def test_multi_digit_page_numbers_parse_correctly(tmp_path: Path) -> None:
    """Page numbers >= 10 must parse correctly (guards against any
    single-digit assumption creeping back into the marker regex/parser)."""
    page_texts = [f"Content for page {i}." for i in range(1, 13)]
    pdf_path = _make_pdf(tmp_path, page_texts)
    result = normalize_pdf(pdf_path)
    pages = list(iter_pages(result))
    assert [p for p, _ in pages] == list(range(1, 13))
    for (_, text), expected in zip(pages, page_texts):
        assert expected in text


def test_normalize_pdf_raises_on_nonexistent_path(tmp_path: Path) -> None:
    missing_path = tmp_path / "does-not-exist.pdf"
    with pytest.raises(Exception):
        normalize_pdf(missing_path)


def test_normalize_pdf_raises_on_corrupt_file(tmp_path: Path) -> None:
    corrupt_path = tmp_path / "corrupt.pdf"
    corrupt_path.write_bytes(b"%PDF-1.4\nnot actually a valid pdf body")
    with pytest.raises(Exception):
        normalize_pdf(corrupt_path)
