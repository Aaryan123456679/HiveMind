"""Tests for `agents.ingestion.normalize_pdf`.

Uses a small multi-page PDF fixture generated at test time via `pymupdf` itself
(no committed binary fixture, no new test-only dependency).
"""

from __future__ import annotations

from pathlib import Path

import fitz
import pytest

from ingestion.normalize_pdf import PAGE_MARKER_RE, normalize_pdf

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
    matches = list(PAGE_MARKER_RE.finditer(result))
    assert len(matches) == len(PAGE_TEXTS)
    for match, expected_text in zip(matches, PAGE_TEXTS):
        assert expected_text in match.group("text")
    # Ensure content did not bleed across page boundaries.
    page1_text = matches[0].group("text")
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
    assert matches[1].group("text").strip() == ""
