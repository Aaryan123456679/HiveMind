"""Support-ticket normalizer: structured JSON/CSV record -> labeled text blob.

Given a single structured support-ticket record -- either a parsed JSON object or a
CSV row (as produced by :class:`csv.DictReader`) -- this module normalizes it into a
common :class:`TicketFields` dataclass, then renders that into a "labeled text blob":
plain text with one ``LABEL: value`` line per scalar field, a verbatim multi-line
``DESCRIPTION:`` section, and an explicit marker-delimited block per comment/reply
(mirroring the ``[[PAGE n]]``/``[[/PAGE n]]`` marker precedent set by
`agents/ingestion/normalize_pdf.py`), so downstream consumers (e.g. the future
`RawDocument` builder in `agents/ingestion/dispatch.py`, issue 3.3.4) can reliably
parse the labeled sections back out and so no structured field is silently dropped.

Ticket schema -- disclosed judgment call
-----------------------------------------
No real support-ticket dataset exists in this repo yet (see `data/README.md`; the real
dataset is issue #19 scope, not yet landed). The field set modeled here follows common
helpdesk/support-ticket system conventions (Zendesk/Freshdesk/Jira-Service-Desk-like):
``ticket_id``, ``subject``, ``description``, ``status``, ``priority``, ``category``,
``requester``, ``assignee`` (optional), ``created_at``, and an optional list of
``comments`` (each with ``author``/``body``). This is a reasonable, disclosed default,
not derived from a concrete existing dataset; it may need revision once a real
support-ticket dataset lands under `data/`.

Two input forms, one shared blob format -- disclosed judgment call
--------------------------------------------------------------------
The issue explicitly scopes both JSON and CSV row input for a single ticket record.
Rather than one dispatching function, this module exposes two small format-specific
entry points -- :func:`normalize_ticket_json` (parsed dict) and
:func:`normalize_ticket_csv_row` (a CSV DictReader row) -- plus a convenience
:func:`normalize_ticket_json_str` wrapper for raw JSON text. Both entry points
converge on the same :class:`TicketFields` dataclass and the same
:func:`render_ticket_blob` builder, so JSON and CSV input for the same logical ticket
produce byte-identical labeled-blob output. A CSV row cannot natively express a nested
list, so the ``comments`` column (if present) is expected to hold a JSON-encoded list
of ``{"author": ..., "body": ...}`` objects -- an explicit, disclosed tradeoff rather
than a silent difference in behavior between the two input forms.

Labeled blob format
--------------------
::

    TICKET_ID: TCK-1001
    SUBJECT: Login page returns 500 error
    STATUS: open
    PRIORITY: high
    CATEGORY: authentication
    REQUESTER: jane.doe@example.com
    ASSIGNEE: support-eng-1@example.com
    CREATED_AT: 2026-07-01T09:15:00Z
    DESCRIPTION:
    Since this morning I can no longer log in.
    The login page shows a generic 500 error after submitting my credentials.
    COMMENTS: 2
    [[COMMENT 1]]
    AUTHOR: support-eng-1@example.com
    BODY:
    Thanks for the report, looking into it now.
    [[/COMMENT 1]]
    [[COMMENT 2]]
    AUTHOR: jane.doe@example.com
    BODY:
    Any update? Still blocked.
    [[/COMMENT 2]]

Every scalar field always emits its ``LABEL:`` line, even when the value is an empty
string (missing optional fields render as ``LABEL: `` rather than being omitted), so a
downstream parser can always find every labeled section. ``COMMENTS: <n>`` is always
present (``n`` may be ``0``) so the comment count is discoverable without scanning for
marker blocks. Comment blocks are 1-indexed and emitted in input order.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field


@dataclass(frozen=True)
class TicketComment:
    """A single comment/reply on a support ticket.

    Attributes:
        author: Comment author identifier (email or name).
        body: Comment text.
    """

    author: str
    body: str


@dataclass(frozen=True)
class TicketFields:
    """Normalized fields extracted from a single structured support-ticket record.

    Attributes:
        ticket_id: Unique ticket identifier.
        subject: Short ticket title/summary.
        description: Full ticket body text (may be multi-line).
        status: Ticket status (e.g. ``open``, ``pending``, ``closed``).
        priority: Ticket priority (e.g. ``low``, ``medium``, ``high``, ``urgent``).
        category: Ticket category/queue.
        requester: Customer/reporter identifier (email or name).
        assignee: Assigned handler identifier; empty string if unassigned.
        created_at: Opaque creation-timestamp string, carried through verbatim (no
            datetime parsing/validation is performed here).
        comments: Ordered tuple of :class:`TicketComment` replies; empty if none.
    """

    ticket_id: str
    subject: str
    description: str
    status: str
    priority: str
    category: str
    requester: str
    assignee: str
    created_at: str
    comments: tuple[TicketComment, ...] = field(default_factory=tuple)


def _str(value: object) -> str:
    """Coerce a possibly-missing/``None`` field value to a plain string."""
    if value is None:
        return ""
    return str(value)


def _comments_from_list(raw_comments: object) -> tuple[TicketComment, ...]:
    """Build a tuple of :class:`TicketComment` from a raw JSON-decoded list value."""
    if not raw_comments:
        return ()
    return tuple(
        TicketComment(author=_str(item.get("author")), body=_str(item.get("body")))
        for item in raw_comments
    )


def normalize_ticket_json(data: dict) -> TicketFields:
    """Normalize a parsed JSON support-ticket object into :class:`TicketFields`.

    Args:
        data: A parsed JSON object (``dict``) for a single ticket record. Optional
            keys (``assignee``, ``comments``) may be absent.

    Returns:
        The normalized :class:`TicketFields`.
    """
    return TicketFields(
        ticket_id=_str(data.get("ticket_id")),
        subject=_str(data.get("subject")),
        description=_str(data.get("description")),
        status=_str(data.get("status")),
        priority=_str(data.get("priority")),
        category=_str(data.get("category")),
        requester=_str(data.get("requester")),
        assignee=_str(data.get("assignee")),
        created_at=_str(data.get("created_at")),
        comments=_comments_from_list(data.get("comments")),
    )


def normalize_ticket_json_str(text: str) -> TicketFields:
    """Parse raw JSON text for a single ticket record and normalize it.

    Convenience wrapper around :func:`normalize_ticket_json` for callers holding raw
    JSON text (e.g. reading a fixture file directly) rather than an already-parsed
    dict.

    Args:
        text: Raw JSON text for a single ticket record.

    Returns:
        The normalized :class:`TicketFields`.

    Raises:
        json.JSONDecodeError: If ``text`` is not valid JSON.
    """
    return normalize_ticket_json(json.loads(text))


def normalize_ticket_csv_row(row: dict) -> TicketFields:
    """Normalize a single CSV support-ticket row into :class:`TicketFields`.

    Args:
        row: A single row dict, as produced by ``csv.DictReader`` (string values,
            keyed by column header). Optional columns (``assignee``, ``comments``) may
            be absent from the header entirely, or present-but-empty.

    Returns:
        The normalized :class:`TicketFields`.

    Raises:
        json.JSONDecodeError: If the ``comments`` column is present and non-empty but
            not valid JSON.
    """
    raw_comments_cell = row.get("comments")
    comments: tuple[TicketComment, ...]
    if raw_comments_cell:
        comments = _comments_from_list(json.loads(raw_comments_cell))
    else:
        comments = ()

    return TicketFields(
        ticket_id=_str(row.get("ticket_id")),
        subject=_str(row.get("subject")),
        description=_str(row.get("description")),
        status=_str(row.get("status")),
        priority=_str(row.get("priority")),
        category=_str(row.get("category")),
        requester=_str(row.get("requester")),
        assignee=_str(row.get("assignee")),
        created_at=_str(row.get("created_at")),
        comments=comments,
    )


def _comment_block(index: int, comment: TicketComment) -> str:
    """Wrap a single comment in its ``[[COMMENT n]]``/``[[/COMMENT n]]`` marker pair."""
    return (
        f"[[COMMENT {index}]]\n"
        f"AUTHOR: {comment.author}\n"
        f"BODY:\n{comment.body}\n"
        f"[[/COMMENT {index}]]\n"
    )


def render_ticket_blob(fields: TicketFields) -> str:
    """Render normalized ticket fields as a labeled text blob.

    See the module docstring for the exact format. Every scalar field always emits
    its ``LABEL:`` line (empty value rather than an omitted line for missing optional
    fields), and ``COMMENTS: <n>`` is always present so the comment count is
    discoverable without scanning for marker blocks.

    Args:
        fields: Normalized ticket fields, from :func:`normalize_ticket_json` or
            :func:`normalize_ticket_csv_row`.

    Returns:
        The labeled text blob.
    """
    lines = [
        f"TICKET_ID: {fields.ticket_id}",
        f"SUBJECT: {fields.subject}",
        f"STATUS: {fields.status}",
        f"PRIORITY: {fields.priority}",
        f"CATEGORY: {fields.category}",
        f"REQUESTER: {fields.requester}",
        f"ASSIGNEE: {fields.assignee}",
        f"CREATED_AT: {fields.created_at}",
        f"DESCRIPTION:\n{fields.description}",
        f"COMMENTS: {len(fields.comments)}",
    ]
    blob = "\n".join(lines) + "\n"
    for index, comment in enumerate(fields.comments, start=1):
        blob += _comment_block(index, comment)
    return blob
