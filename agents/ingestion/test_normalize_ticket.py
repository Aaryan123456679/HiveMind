"""Tests `agents.ingestion.normalize_ticket`.

Fixtures under `agents/ingestion/testdata/` are hand-authored structured records (JSON
and CSV): `ticket_sample_1.json`/`ticket_sample_1.csv` encode the same logical ticket
record (used for the JSON/CSV parity test), and `ticket_sample_2_minimal.csv` omits the
optional `assignee`/`comments` columns entirely, to exercise the missing-optional-field
path. No generation script is used, since JSON/CSV -- like the Enron `.txt` fixtures
used by `test_normalize_email.py` -- are plain, hand-writable text formats.
"""

from __future__ import annotations

import csv
import json
from pathlib import Path

from ingestion.normalize_ticket import (
    TicketFields,
    normalize_ticket_csv_row,
    normalize_ticket_json,
    normalize_ticket_json_str,
    render_ticket_blob,
)

TESTDATA_DIR = Path(__file__).parent / "testdata"

FIXTURE_JSON = TESTDATA_DIR / "ticket_sample_1.json"
FIXTURE_CSV = TESTDATA_DIR / "ticket_sample_1.csv"
FIXTURE_CSV_MINIMAL = TESTDATA_DIR / "ticket_sample_2_minimal.csv"


def _read_csv_row(path: Path) -> dict:
    with path.open(newline="", encoding="utf-8") as fh:
        reader = csv.DictReader(fh)
        return next(reader)


def test_json_scalar_fields_extracted() -> None:
    fields = normalize_ticket_json_str(FIXTURE_JSON.read_text(encoding="utf-8"))
    assert fields.ticket_id == "TCK-1001"
    assert fields.subject == "Login page returns 500 error"
    assert fields.status == "open"
    assert fields.priority == "high"
    assert fields.category == "authentication"
    assert fields.requester == "jane.doe@example.com"
    assert fields.assignee == "support-eng-1@example.com"
    assert fields.created_at == "2026-07-01T09:15:00Z"
    assert "no longer log in" in fields.description


def test_json_comments_extracted_in_order() -> None:
    fields = normalize_ticket_json_str(FIXTURE_JSON.read_text(encoding="utf-8"))
    assert len(fields.comments) == 2
    assert fields.comments[0].author == "support-eng-1@example.com"
    assert "looking into it" in fields.comments[0].body
    assert fields.comments[1].author == "jane.doe@example.com"
    assert "Still blocked" in fields.comments[1].body


def test_normalize_ticket_json_str_from_fixture_file() -> None:
    text = FIXTURE_JSON.read_text(encoding="utf-8")
    fields = normalize_ticket_json_str(text)
    assert isinstance(fields, TicketFields)
    assert fields.ticket_id == "TCK-1001"


def test_csv_scalar_fields_extracted() -> None:
    row = _read_csv_row(FIXTURE_CSV)
    fields = normalize_ticket_csv_row(row)
    assert fields.ticket_id == "TCK-1001"
    assert fields.subject == "Login page returns 500 error"
    assert fields.status == "open"
    assert fields.priority == "high"
    assert fields.requester == "jane.doe@example.com"
    assert fields.assignee == "support-eng-1@example.com"
    assert len(fields.comments) == 2
    assert fields.comments[1].author == "jane.doe@example.com"


def test_csv_missing_optional_fields_does_not_raise() -> None:
    row = _read_csv_row(FIXTURE_CSV_MINIMAL)
    fields = normalize_ticket_csv_row(row)
    assert fields.ticket_id == "TCK-1002"
    assert fields.assignee == ""
    assert fields.comments == ()

    blob = render_ticket_blob(fields)
    assert "ASSIGNEE: \n" in blob
    assert "COMMENTS: 0\n" in blob
    assert "[[COMMENT" not in blob


def test_json_and_csv_produce_identical_blob_for_same_record() -> None:
    json_fields = normalize_ticket_json_str(FIXTURE_JSON.read_text(encoding="utf-8"))
    csv_fields = normalize_ticket_csv_row(_read_csv_row(FIXTURE_CSV))
    assert render_ticket_blob(json_fields) == render_ticket_blob(csv_fields)


def test_blob_contains_all_labeled_scalar_sections() -> None:
    fields = normalize_ticket_json_str(FIXTURE_JSON.read_text(encoding="utf-8"))
    blob = render_ticket_blob(fields)
    for label in (
        "TICKET_ID:",
        "SUBJECT:",
        "STATUS:",
        "PRIORITY:",
        "CATEGORY:",
        "REQUESTER:",
        "ASSIGNEE:",
        "CREATED_AT:",
        "DESCRIPTION:",
        "COMMENTS:",
    ):
        assert label in blob


def test_blob_description_section_multiline() -> None:
    fields = normalize_ticket_json_str(FIXTURE_JSON.read_text(encoding="utf-8"))
    blob = render_ticket_blob(fields)
    assert "DESCRIPTION:\nSince this morning I can no longer log in." in blob
    assert "This is blocking my whole team." in blob


def test_blob_comments_rendered_as_marker_blocks_in_order() -> None:
    fields = normalize_ticket_json_str(FIXTURE_JSON.read_text(encoding="utf-8"))
    blob = render_ticket_blob(fields)
    first_idx = blob.index("[[COMMENT 1]]")
    first_close_idx = blob.index("[[/COMMENT 1]]")
    second_idx = blob.index("[[COMMENT 2]]")
    second_close_idx = blob.index("[[/COMMENT 2]]")
    assert first_idx < first_close_idx < second_idx < second_close_idx
    assert "AUTHOR: support-eng-1@example.com" in blob
    assert "looking into it now." in blob
    assert "AUTHOR: jane.doe@example.com" in blob
    assert "Still blocked" in blob


def test_blob_zero_comments_header_present() -> None:
    fields = TicketFields(
        ticket_id="TCK-9",
        subject="s",
        description="d",
        status="open",
        priority="low",
        category="c",
        requester="r",
        assignee="",
        created_at="t",
        comments=(),
    )
    blob = render_ticket_blob(fields)
    assert "COMMENTS: 0" in blob
    assert "[[COMMENT" not in blob


def test_all_input_field_values_present_in_blob() -> None:
    """End-to-end 'preserving all structured fields' acceptance criterion."""
    data = json.loads(FIXTURE_JSON.read_text(encoding="utf-8"))
    fields = normalize_ticket_json(data)
    blob = render_ticket_blob(fields)

    for key in (
        "ticket_id",
        "subject",
        "status",
        "priority",
        "category",
        "requester",
        "assignee",
        "created_at",
    ):
        assert str(data[key]) in blob

    for comment in data["comments"]:
        assert comment["author"] in blob
        for line in comment["body"].splitlines():
            assert line in blob
