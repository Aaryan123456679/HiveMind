"""Normalizer dispatch by `sourceType`: adapts all three normalizers into `RawDocument`.

Issue #17 subtask 3.3.4 (final subtask of issue #17): a dispatch function selects the
correct normalizer given a `sourceType` and produces a common `RawDocument` (see
`agents/ingestion/rawdoc.py`) regardless of source type.

Dispatch signature -- disclosed design
-----------------------------------------
The three source types genuinely need different input shapes (a file path for PDF, a
file path for email, a parsed JSON dict OR a CSV row dict for ticket -- the ticket
normalizer itself already exposes two entry points for this reason, see
`normalize_ticket.py`). Rather than a single dispatch function with a source-type-
dependent, loosely-typed "payload" parameter, this module exposes:

- Four small convenience wrappers, one per concrete input shape: :func:`dispatch_pdf`,
  :func:`dispatch_email`, :func:`dispatch_ticket_json`, :func:`dispatch_ticket_csv_row`.
  Each has a precise, source-appropriate signature and is independently unit-testable.
- One core :func:`dispatch` function satisfying the issue's literal acceptance
  criterion ("a dispatch function selects the correct normalizer given a sourceType"):
  it takes an explicit `source_type` plus the union of possible keyword inputs
  (`path`, `data`, `row`), validates that exactly the right keyword(s) were supplied
  for the given `source_type`, and delegates to the matching convenience wrapper
  above. This keeps a single true dispatch entry point (as the acceptance criteria
  literally requires and as `test_dispatch.py` exercises) while keeping each concrete
  code path small, precisely typed, and independently testable.
"""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path

from ingestion.normalize_email import EmailFields, normalize_email
from ingestion.normalize_pdf import normalize_pdf
from ingestion.normalize_ticket import (
    TicketFields,
    normalize_ticket_csv_row,
    normalize_ticket_json,
    render_ticket_blob,
)
from ingestion.rawdoc import RawDocument, SourceType


def _resolve_timestamp(timestamp: datetime | None) -> datetime:
    """Return `timestamp` if given, else the current UTC time."""
    return timestamp if timestamp is not None else datetime.now(timezone.utc)


def _email_structured_fields(fields: EmailFields) -> dict[str, object]:
    return {
        "sender": fields.sender,
        "subject": fields.subject,
        "thread": fields.thread,
    }


def _ticket_structured_fields(fields: TicketFields) -> dict[str, object]:
    """Scalar ticket metadata for `structured_fields`.

    Comment *bodies* are intentionally not duplicated here -- they already live in
    `text` (the rendered labeled blob's marker-delimited comment blocks). Only the
    comment *count* is surfaced, so callers can tell whether comments exist without
    duplicating their content in a second representation that could drift from `text`.
    """
    return {
        "ticket_id": fields.ticket_id,
        "subject": fields.subject,
        "status": fields.status,
        "priority": fields.priority,
        "category": fields.category,
        "requester": fields.requester,
        "assignee": fields.assignee,
        "created_at": fields.created_at,
        "comment_count": len(fields.comments),
    }


def dispatch_pdf(
    doc_id: str, path: str | Path, *, timestamp: datetime | None = None
) -> RawDocument:
    """Normalize a PDF file and wrap it as a `RawDocument`.

    Args:
        doc_id: Caller-supplied document id, carried through verbatim.
        path: Path to the PDF file, as accepted by `normalize_pdf`.
        timestamp: Optional explicit ingestion timestamp; defaults to UTC-now.

    Returns:
        A `RawDocument` with `source_type="pdf"`, `text` set to `normalize_pdf`'s
        marker-delimited page text, and `structured_fields={"page_count": n}`.
    """
    text = normalize_pdf(path)
    page_count = text.page_count
    return RawDocument(
        id=doc_id,
        source_type="pdf",
        text=text,
        structured_fields={"page_count": page_count},
        timestamp=_resolve_timestamp(timestamp),
    )


def dispatch_email(
    doc_id: str, path: str | Path, *, timestamp: datetime | None = None
) -> RawDocument:
    """Normalize a raw Enron-format email file and wrap it as a `RawDocument`.

    Args:
        doc_id: Caller-supplied document id, carried through verbatim.
        path: Path to the raw message file, as accepted by `normalize_email`.
        timestamp: Optional explicit ingestion timestamp; defaults to UTC-now.

    Returns:
        A `RawDocument` with `source_type="email"`, `text` set to the message body,
        and `structured_fields={"sender", "subject", "thread"}`.
    """
    fields = normalize_email(path)
    return RawDocument(
        id=doc_id,
        source_type="email",
        text=fields.body,
        structured_fields=_email_structured_fields(fields),
        timestamp=_resolve_timestamp(timestamp),
    )


def dispatch_ticket_json(
    doc_id: str, data: dict, *, timestamp: datetime | None = None
) -> RawDocument:
    """Normalize a parsed JSON ticket record and wrap it as a `RawDocument`.

    Args:
        doc_id: Caller-supplied document id, carried through verbatim.
        data: Parsed JSON object for a single ticket record, as accepted by
            `normalize_ticket_json`.
        timestamp: Optional explicit ingestion timestamp; defaults to UTC-now.

    Returns:
        A `RawDocument` with `source_type="ticket"`, `text` set to the rendered
        labeled blob (`render_ticket_blob`), and `structured_fields` holding the
        scalar ticket metadata plus `comment_count`.
    """
    fields = normalize_ticket_json(data)
    return RawDocument(
        id=doc_id,
        source_type="ticket",
        text=render_ticket_blob(fields),
        structured_fields=_ticket_structured_fields(fields),
        timestamp=_resolve_timestamp(timestamp),
    )


def dispatch_ticket_csv_row(
    doc_id: str, row: dict, *, timestamp: datetime | None = None
) -> RawDocument:
    """Normalize a CSV ticket row and wrap it as a `RawDocument`.

    Args:
        doc_id: Caller-supplied document id, carried through verbatim.
        row: A single CSV row dict, as accepted by `normalize_ticket_csv_row`.
        timestamp: Optional explicit ingestion timestamp; defaults to UTC-now.

    Returns:
        A `RawDocument` with `source_type="ticket"`, `text` set to the rendered
        labeled blob (`render_ticket_blob`), and `structured_fields` holding the
        scalar ticket metadata plus `comment_count`.
    """
    fields = normalize_ticket_csv_row(row)
    return RawDocument(
        id=doc_id,
        source_type="ticket",
        text=render_ticket_blob(fields),
        structured_fields=_ticket_structured_fields(fields),
        timestamp=_resolve_timestamp(timestamp),
    )


def dispatch(
    source_type: SourceType,
    doc_id: str,
    *,
    path: str | Path | None = None,
    data: dict | None = None,
    row: dict | None = None,
    timestamp: datetime | None = None,
) -> RawDocument:
    """Select the correct normalizer for `source_type` and produce a `RawDocument`.

    Exactly one of `path` (for `"pdf"`/`"email"`), `data` (for `"ticket"` from a
    parsed JSON object), or `row` (for `"ticket"` from a CSV row) must be supplied,
    matching `source_type`; supplying the wrong keyword(s) for a given `source_type`,
    or an unrecognized `source_type`, raises `ValueError`.

    Args:
        source_type: One of `"pdf"`, `"email"`, `"ticket"` (see
            `ingestion.rawdoc.SourceType`).
        doc_id: Caller-supplied document id, carried through verbatim.
        path: PDF or email file path (required for `source_type="pdf"` or
            `"email"`, disallowed otherwise).
        data: Parsed JSON ticket object (allowed only for `source_type="ticket"`,
            mutually exclusive with `row`).
        row: CSV ticket row dict (allowed only for `source_type="ticket"`, mutually
            exclusive with `data`).
        timestamp: Optional explicit ingestion timestamp; defaults to UTC-now.

    Returns:
        The `RawDocument` produced by the normalizer selected for `source_type`.

    Raises:
        ValueError: If `source_type` is not one of `"pdf"`, `"email"`, `"ticket"`,
            or if the supplied keyword arguments do not match what `source_type`
            requires.
    """
    if source_type == "pdf":
        if path is None or data is not None or row is not None:
            raise ValueError("source_type='pdf' requires exactly `path`.")
        return dispatch_pdf(doc_id, path, timestamp=timestamp)

    if source_type == "email":
        if path is None or data is not None or row is not None:
            raise ValueError("source_type='email' requires exactly `path`.")
        return dispatch_email(doc_id, path, timestamp=timestamp)

    if source_type == "ticket":
        if path is not None or (data is None) == (row is None):
            raise ValueError(
                "source_type='ticket' requires exactly one of `data` or `row`."
            )
        if data is not None:
            return dispatch_ticket_json(doc_id, data, timestamp=timestamp)
        return dispatch_ticket_csv_row(doc_id, row, timestamp=timestamp)

    raise ValueError(
        f"Unknown source_type {source_type!r}; expected 'pdf', 'email', or 'ticket'."
    )
