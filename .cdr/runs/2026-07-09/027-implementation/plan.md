# Plan

## `agents/ingestion/rawdoc.py`

- `SourceType = Literal["pdf", "email", "ticket"]` — exact string values, disclosed.
- `@dataclass(frozen=True) class RawDocument`: fields `id: str`, `source_type: SourceType`,
  `text: str`, `structured_fields: dict[str, object]`, `timestamp: datetime` — snake_case,
  deviating from the issue's literal camelCase (`sourceType`, `structuredFields`) to match this
  package's established snake_case convention (`EmailFields`, `TicketFields`). Disclosed.
- No behavior beyond being a plain immutable record — all field-adaptation logic lives in
  `dispatch.py` so `rawdoc.py` stays a pure, dependency-free record type (mirrors
  `TicketComment`/`TicketFields`'s pure-dataclass style).

## `agents/ingestion/dispatch.py`

- Three small private adapters, one per source type, each returning `(text, structured_fields)`:
  - `_pdf_to_raw(path) -> (text, fields)`: `text = normalize_pdf(path)`; `fields =
    {"page_count": <count via iter_pages>}`.
  - `_email_to_raw(path) -> (text, fields)`: `fields = EmailFields`, `text = fields.body`,
    `structured_fields = {"sender", "subject", "thread"}`.
  - `_ticket_to_raw(fields: TicketFields) -> (text, fields_dict)`: `text =
    render_ticket_blob(fields)`, `structured_fields` = all TicketFields scalars + comment_count
    (comments themselves already embedded in `text`'s marker blocks, not duplicated as objects
    in `structured_fields`, keeping it JSON-serializable-scalar-only by convention).
- Convenience wrappers `dispatch_pdf`, `dispatch_email`, `dispatch_ticket_json`,
  `dispatch_ticket_csv_row` — each takes `(doc_id, <source-specific input>, timestamp=None)`,
  calls the matching normalizer + adapter, and builds a `RawDocument`.
- Core `dispatch(source_type, doc_id, *, path=None, data=None, row=None, timestamp=None)` —
  single entry point satisfying the acceptance criterion "a dispatch function selects the correct
  normalizer given a sourceType"; routes to the wrappers above based on `source_type`, validating
  that the right keyword(s) were supplied for that source type (raises `ValueError` otherwise).
- `timestamp` defaults to `datetime.now(timezone.utc)` at call time if not supplied.

## `agents/ingestion/test_dispatch.py`

- Reuse existing fixtures under `agents/ingestion/testdata/` (`enron_sample_1.txt`,
  `ticket_sample_1.json`, `ticket_sample_1.csv`) plus a PDF built at test time via `fitz`
  (matching `test_normalize_pdf.py`'s existing pattern — no new binary fixture).
- Test matrix (see validation-matrix.json) covering: `RawDocument` shape consistency across all
  three source types via both `dispatch()` and the convenience wrappers, correct normalizer
  invoked per `source_type`, `structured_fields` content per source, error handling for
  unknown `source_type` / missing required kwargs / wrong kwargs for a given type.

## Marker-utility refactor

Not performed — see architecture-discovery.md. No changes to `normalize_pdf.py` /
`normalize_ticket.py`.
