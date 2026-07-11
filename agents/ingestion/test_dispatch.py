"""Tests for `ingestion.dispatch` and `ingestion.rawdoc.RawDocument`.

Exercises `dispatch()` (and its convenience wrappers) across all three source types
per issue #17 subtask 3.3.4's test spec: feed one fixture of each sourceType through
the dispatcher, assert the correct normalizer is invoked and the `RawDocument` shape
is consistent across all three.

PDF fixtures are built at test time via `fitz`, mirroring `test_normalize_pdf.py`'s
existing pattern (no committed binary fixture). Email/ticket fixtures reuse the
existing hand-authored files under `agents/ingestion/testdata/`.
"""

from __future__ import annotations

import csv
import json
from datetime import datetime, timezone
from pathlib import Path

import fitz
import pytest

from ingestion import dispatch as dispatch_module
from ingestion.dispatch import (
    dispatch,
    dispatch_email,
    dispatch_pdf,
    dispatch_ticket_csv_row,
    dispatch_ticket_json,
)
from ingestion.rawdoc import RawDocument

TESTDATA_DIR = Path(__file__).parent / "testdata"
FIXTURE_EMAIL = TESTDATA_DIR / "enron_sample_1.txt"
FIXTURE_TICKET_JSON = TESTDATA_DIR / "ticket_sample_1.json"
FIXTURE_TICKET_CSV = TESTDATA_DIR / "ticket_sample_1.csv"

PDF_PAGE_TEXTS = ["Page one content.", "Page two content."]


def _make_pdf(tmp_path: Path, page_texts: list[str]) -> Path:
    """Build a PDF with one page per string in `page_texts`."""
    doc = fitz.open()
    try:
        for text in page_texts:
            page = doc.new_page()
            page.insert_text((72, 72), text)
    finally:
        pdf_path = tmp_path / "fixture.pdf"
        doc.save(str(pdf_path))
        doc.close()
    return pdf_path


def _read_csv_row(path: Path) -> dict:
    with path.open(newline="", encoding="utf-8") as fh:
        reader = csv.DictReader(fh)
        return next(reader)


def _ticket_json_data() -> dict:
    return json.loads(FIXTURE_TICKET_JSON.read_text(encoding="utf-8"))


# ---------------------------------------------------------------------------
# RawDocument shape
# ---------------------------------------------------------------------------


def test_dispatch_pdf_returns_rawdocument_shape(tmp_path: Path) -> None:
    pdf_path = _make_pdf(tmp_path, PDF_PAGE_TEXTS)
    doc = dispatch_pdf("doc-1", pdf_path)
    assert isinstance(doc, RawDocument)
    assert doc.id == "doc-1"
    assert doc.source_type == "pdf"
    assert isinstance(doc.text, str) and doc.text
    assert isinstance(doc.structured_fields, dict)
    assert isinstance(doc.timestamp, datetime)


def test_dispatch_email_returns_rawdocument_shape() -> None:
    doc = dispatch_email("doc-2", FIXTURE_EMAIL)
    assert isinstance(doc, RawDocument)
    assert doc.id == "doc-2"
    assert doc.source_type == "email"
    assert isinstance(doc.text, str) and doc.text
    assert isinstance(doc.structured_fields, dict)
    assert isinstance(doc.timestamp, datetime)


def test_dispatch_ticket_json_returns_rawdocument_shape() -> None:
    doc = dispatch_ticket_json("doc-3", _ticket_json_data())
    assert isinstance(doc, RawDocument)
    assert doc.id == "doc-3"
    assert doc.source_type == "ticket"
    assert isinstance(doc.text, str) and doc.text
    assert isinstance(doc.structured_fields, dict)
    assert isinstance(doc.timestamp, datetime)


def test_dispatch_ticket_csv_returns_rawdocument_shape() -> None:
    doc = dispatch_ticket_csv_row("doc-4", _read_csv_row(FIXTURE_TICKET_CSV))
    assert isinstance(doc, RawDocument)
    assert doc.id == "doc-4"
    assert doc.source_type == "ticket"
    assert isinstance(doc.text, str) and doc.text
    assert isinstance(doc.structured_fields, dict)
    assert isinstance(doc.timestamp, datetime)


def test_rawdocument_shape_consistent_across_source_types(tmp_path: Path) -> None:
    pdf_path = _make_pdf(tmp_path, PDF_PAGE_TEXTS)
    docs = [
        dispatch_pdf("d", pdf_path),
        dispatch_email("d", FIXTURE_EMAIL),
        dispatch_ticket_json("d", _ticket_json_data()),
    ]
    expected_fields = {"id", "source_type", "text", "structured_fields", "timestamp"}
    for doc in docs:
        assert {f.name for f in doc.__dataclass_fields__.values()} == expected_fields


# ---------------------------------------------------------------------------
# structured_fields / text content
# ---------------------------------------------------------------------------


def test_dispatch_pdf_structured_fields_page_count(tmp_path: Path) -> None:
    pdf_path = _make_pdf(tmp_path, PDF_PAGE_TEXTS)
    doc = dispatch_pdf("doc-1", pdf_path)
    assert doc.structured_fields["page_count"] == len(PDF_PAGE_TEXTS)


def test_dispatch_pdf_page_count_matches_normalize_pdf_no_second_parse(
    tmp_path: Path, monkeypatch
) -> None:
    """Issue #53 subtask 4.5.15.5: dispatch_pdf must read `page_count` directly off
    `normalize_pdf`'s result instead of re-deriving it via a second `iter_pages` pass.

    Monkeypatches `iter_pages` in the `ingestion.normalize_pdf` module (where
    `normalize_pdf` would call it from, were it still doing the redundant second pass)
    to raise if invoked at all, then asserts `dispatch_pdf` still reports the correct
    page count -- proving both correctness and that no second parse occurs.
    """
    from ingestion import normalize_pdf as normalize_pdf_module

    def _iter_pages_should_not_be_called(*args, **kwargs):
        raise AssertionError(
            "iter_pages should not be called by dispatch_pdf -- page_count must come "
            "directly from normalize_pdf's result, not a redundant second parse."
        )

    monkeypatch.setattr(
        normalize_pdf_module, "iter_pages", _iter_pages_should_not_be_called
    )
    pdf_path = _make_pdf(tmp_path, PDF_PAGE_TEXTS)

    doc = dispatch_pdf("doc-1", pdf_path)

    assert doc.structured_fields["page_count"] == len(PDF_PAGE_TEXTS)


def test_dispatch_email_structured_fields_and_text() -> None:
    doc = dispatch_email("doc-2", FIXTURE_EMAIL)
    assert doc.structured_fields["sender"] == "phillip.allen@enron.com"
    assert doc.structured_fields["subject"] == "Q2 gas storage numbers"
    assert "thread" in doc.structured_fields
    assert "Phillip" in doc.text


def test_dispatch_ticket_structured_fields_and_text() -> None:
    doc = dispatch_ticket_json("doc-3", _ticket_json_data())
    assert doc.structured_fields["ticket_id"] == "TCK-1001"
    assert doc.structured_fields["status"] == "open"
    assert doc.structured_fields["priority"] == "high"
    assert doc.structured_fields["comment_count"] == 2
    assert "TICKET_ID: TCK-1001" in doc.text
    assert "[[COMMENT 1 LEN=" in doc.text


# ---------------------------------------------------------------------------
# Correct normalizer invoked (spy via monkeypatch)
# ---------------------------------------------------------------------------


def test_dispatch_pdf_invokes_normalize_pdf(tmp_path: Path, monkeypatch) -> None:
    pdf_path = _make_pdf(tmp_path, PDF_PAGE_TEXTS)
    calls = []
    original = dispatch_module.normalize_pdf

    def spy(path):
        calls.append(path)
        return original(path)

    monkeypatch.setattr(dispatch_module, "normalize_pdf", spy)
    dispatch("pdf", "doc-1", path=pdf_path)
    assert calls == [pdf_path]


def test_dispatch_email_invokes_normalize_email(monkeypatch) -> None:
    calls = []
    original = dispatch_module.normalize_email

    def spy(path):
        calls.append(path)
        return original(path)

    monkeypatch.setattr(dispatch_module, "normalize_email", spy)
    dispatch("email", "doc-2", path=FIXTURE_EMAIL)
    assert calls == [FIXTURE_EMAIL]


def _empty_ticket_fields():
    return dispatch_module.TicketFields(
        ticket_id="",
        subject="",
        description="",
        status="",
        priority="",
        category="",
        requester="",
        assignee="",
        created_at="",
    )


def test_dispatch_ticket_json_invokes_normalize_ticket_json(monkeypatch) -> None:
    json_calls = []
    csv_calls = []

    def json_spy(data):
        json_calls.append(data)
        return _empty_ticket_fields()

    monkeypatch.setattr(dispatch_module, "normalize_ticket_json", json_spy)
    monkeypatch.setattr(
        dispatch_module,
        "normalize_ticket_csv_row",
        lambda row: csv_calls.append(row),
    )
    dispatch("ticket", "doc-3", data=_ticket_json_data())
    assert len(json_calls) == 1
    assert csv_calls == []


def test_dispatch_ticket_csv_invokes_normalize_ticket_csv_row(monkeypatch) -> None:
    json_calls = []
    csv_calls = []
    row = _read_csv_row(FIXTURE_TICKET_CSV)

    monkeypatch.setattr(
        dispatch_module,
        "normalize_ticket_json",
        lambda data: json_calls.append(data),
    )
    original_csv = dispatch_module.normalize_ticket_csv_row

    def csv_spy(r):
        csv_calls.append(r)
        return original_csv(r)

    monkeypatch.setattr(dispatch_module, "normalize_ticket_csv_row", csv_spy)
    dispatch("ticket", "doc-4", row=row)
    assert csv_calls == [row]
    assert json_calls == []


# ---------------------------------------------------------------------------
# Error handling
# ---------------------------------------------------------------------------


def test_dispatch_unknown_source_type_raises() -> None:
    with pytest.raises(ValueError):
        dispatch("csv", "doc-5", path="whatever")  # type: ignore[arg-type]


def test_dispatch_ticket_missing_input_raises() -> None:
    with pytest.raises(ValueError):
        dispatch("ticket", "doc-6")


def test_dispatch_ticket_both_inputs_raises() -> None:
    with pytest.raises(ValueError):
        dispatch("ticket", "doc-7", data=_ticket_json_data(), row={"ticket_id": "x"})


def test_dispatch_pdf_missing_path_raises() -> None:
    with pytest.raises(ValueError):
        dispatch("pdf", "doc-8")


def test_dispatch_email_missing_path_raises() -> None:
    with pytest.raises(ValueError):
        dispatch("email", "doc-9")


def test_dispatch_pdf_rejects_ticket_kwargs(tmp_path: Path) -> None:
    pdf_path = _make_pdf(tmp_path, PDF_PAGE_TEXTS)
    with pytest.raises(ValueError):
        dispatch("pdf", "doc-10", path=pdf_path, data={})


# ---------------------------------------------------------------------------
# JSON/CSV parity via dispatch
# ---------------------------------------------------------------------------


def test_dispatch_ticket_json_and_csv_parity() -> None:
    json_doc = dispatch_ticket_json("same-id", _ticket_json_data())
    csv_doc = dispatch_ticket_csv_row("same-id", _read_csv_row(FIXTURE_TICKET_CSV))
    assert json_doc.text == csv_doc.text
    assert json_doc.structured_fields == csv_doc.structured_fields


# ---------------------------------------------------------------------------
# id / timestamp handling
# ---------------------------------------------------------------------------


def test_dispatch_id_passthrough_all_types(tmp_path: Path) -> None:
    pdf_path = _make_pdf(tmp_path, PDF_PAGE_TEXTS)
    assert dispatch_pdf("my-custom-id", pdf_path).id == "my-custom-id"
    assert dispatch_email("my-custom-id", FIXTURE_EMAIL).id == "my-custom-id"
    assert dispatch_ticket_json("my-custom-id", _ticket_json_data()).id == "my-custom-id"


def test_dispatch_explicit_timestamp_used() -> None:
    explicit = datetime(2020, 1, 1, tzinfo=timezone.utc)
    doc = dispatch_email("doc-2", FIXTURE_EMAIL, timestamp=explicit)
    assert doc.timestamp == explicit


def test_dispatch_default_timestamp_is_utc_aware() -> None:
    before = datetime.now(timezone.utc)
    doc = dispatch_email("doc-2", FIXTURE_EMAIL)
    after = datetime.now(timezone.utc)
    assert doc.timestamp.tzinfo is not None
    assert before <= doc.timestamp <= after
