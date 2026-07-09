"""Common `RawDocument` record type shared by all `agents/ingestion/` normalizers.

`RawDocument` is the single, source-type-agnostic shape that
`agents/ingestion/dispatch.py` produces regardless of which underlying normalizer
(`normalize_pdf`, `normalize_email`, `normalize_ticket`) built it, per issue #17
subtask 3.3.4. It is the stable hand-off contract into the (not-yet-built)
segmentation agent described in `docs/LLD/ingestion-agent.md`.

Field naming -- disclosed deviation from the issue's literal camelCase
------------------------------------------------------------------------
The GitHub issue and `docs/LLD/ingestion-agent.md` describe the shape as
``RawDocument{id, sourceType, text, structuredFields, timestamp}`` (camelCase). This
module instead uses snake_case field names (``source_type``, ``structured_fields``)
to match this exact package's established Python naming convention, as evidenced by
the already-shipped, already-verified sibling dataclasses ``EmailFields`` (3.3.2:
``sender``, ``subject``, ``thread``, ``body``) and ``TicketFields`` (3.3.3:
``ticket_id``, ``created_at``, etc.) in `normalize_email.py` / `normalize_ticket.py`.
The camelCase in the issue/LLD is read as describing the conceptual/wire shape, not
dictating Python identifier casing. The field *set* and semantics are unchanged from
the issue's literal specification (``id``, ``sourceType`` -> ``source_type``,
``text``, ``structuredFields`` -> ``structured_fields``, ``timestamp``).

``source_type`` values -- disclosed choice
-------------------------------------------
Exactly three lowercase string literals, matching the three normalizers this issue
scopes: ``"pdf"``, ``"email"``, ``"ticket"``. See :data:`SourceType`.

``id`` / ``timestamp`` -- disclosed design
---------------------------------------------
Neither `normalize_pdf.py` nor `normalize_email.py` produces a natural, always-present
document identifier (unlike ticket's ``ticket_id``, which is source data, not an
ingestion-pipeline concept), and none of the three normalizers produces an
ingestion timestamp. Rather than inventing an unreliable per-source-type id/timestamp
derivation, both are caller-supplied at the `dispatch.py` layer: ``id`` is required
from the caller for every source type (for uniform, predictable behavior across
sources), and ``timestamp`` defaults to ingestion-time UTC-now if the caller does not
supply one explicitly (see `dispatch.py`).
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import Literal

#: The three normalizer source types this issue (#17) scopes. Exact string values
#: used as the `source_type` discriminant across `RawDocument` and `dispatch.py`.
SourceType = Literal["pdf", "email", "ticket"]


@dataclass(frozen=True)
class RawDocument:
    """Common record type produced by every `agents/ingestion/` normalizer.

    See the module docstring for the disclosed naming deviation from the issue's
    literal camelCase field names, and for the ``id``/``timestamp`` design.

    Attributes:
        id: Caller-supplied document identifier, carried through verbatim. Stable
            across re-ingestion of the same source document is the caller's
            responsibility, not derived here.
        source_type: Which normalizer produced this document. One of ``"pdf"``,
            ``"email"``, ``"ticket"`` (see :data:`SourceType`).
        text: The normalizer's rendered text payload -- `normalize_pdf`'s
            marker-delimited page text, `normalize_email`'s message body, or
            `normalize_ticket`'s labeled text blob (`render_ticket_blob` output).
            This is the primary content downstream segmentation operates on.
        structured_fields: Source-specific metadata extracted alongside `text` (e.g.
            email's sender/subject/thread, ticket's ticket_id/status/priority/etc,
            PDF's page count), as a plain JSON-serializable-scalar dict. Does not
            duplicate information already fully represented in `text` (e.g. ticket
            comment bodies, which live only in the rendered blob).
        timestamp: Ingestion-time UTC timestamp (or a caller-supplied override), not
            a property of the source document itself.
    """

    id: str
    source_type: SourceType
    text: str
    structured_fields: dict[str, object]
    timestamp: datetime
