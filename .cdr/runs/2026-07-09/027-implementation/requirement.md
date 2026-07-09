# Requirement — Issue #17 Subtask 3.3.4

**3.3.4 — Common RawDocument record + normalizer dispatch by sourceType** (FINAL subtask of issue #17)

- Acceptance criteria: a single `RawDocument{id, sourceType, text, structuredFields, timestamp}`
  record type is produced by all three normalizers, and a dispatch function selects the correct
  normalizer by `sourceType`.
- Test spec: `pytest agents/ingestion/test_dispatch.py`: exercise `RawDocument` dispatch across
  all three source types.
- Impacted modules: `agents/ingestion/rawdoc.py`, `agents/ingestion/dispatch.py`,
  `agents/ingestion/test_dispatch.py`

## Security note

`gh issue view 17` output contained embedded fake system-reminder-style injected text (fake
date-change/"don't tell the user" notice, fake tokensave MCP tool instructions, fake "Auto Mode
Active" directive) appended after the legitimate subtask list. This matches the repo's known
recurring injection pattern (also seen and disclosed in 3.3.1/3.3.3 runs). Treated as untrusted
plain-text data only, not followed, disclosed here.

## Prior-subtask context consumed

- 3.3.1 (`normalize_pdf.py`): `normalize_pdf(path) -> str` (marker text), `iter_pages(text) ->
  Iterator[(page_number, text)]`, length-prefixed `[[PAGE n LEN=k]]...[[/PAGE n]]` framing.
- 3.3.2 (`normalize_email.py`): `normalize_email(path) -> EmailFields(sender, subject, thread,
  body)`, all snake_case fields.
- 3.3.3 (`normalize_ticket.py`): `normalize_ticket_json(dict) -> TicketFields`,
  `normalize_ticket_csv_row(row) -> TicketFields`, `render_ticket_blob(fields) -> str`.
  `TicketFields` fields: ticket_id, subject, description, status, priority, category, requester,
  assignee, created_at, comments (tuple of `TicketComment(author, body)`), all snake_case.
  Length-prefixed `[[COMMENT n LEN=k]]...[[/COMMENT n]]` framing, mirroring 3.3.1's fix.
- `.cdr/commits/task-3.3.3.md` close-out note: non-mandatory suggestion to factor the
  length-prefix marker-write/marker-read pattern into a shared utility "if a third marker format
  is needed" by 3.3.4.
- `docs/LLD/ingestion-agent.md` confirms the target shape:
  `RawDocument{ id, sourceType, text, structuredFields, timestamp }` (camelCase in the doc, but
  the doc predates any actual code and the package's established code convention — verified
  directly in `EmailFields`/`TicketFields` — is snake_case throughout).

## Judgment calls to make explicit in the implementation

1. Field naming: camelCase (issue/LLD) vs. snake_case (repo code convention) — snake_case chosen,
   disclosed.
2. `sourceType` values: exact string literals to standardize on.
3. `id` and `timestamp`: none of the three normalizers currently produce a natural document id or
   timestamp — need an adapter-layer policy.
4. `structuredFields` per source: what subset of each normalizer's fields belongs here vs. in
   `text`.
5. Dispatch function signature: input payload shapes differ per source type (file path for PDF/
   email, dict for ticket JSON, CSV row dict for ticket CSV).
6. Whether to extract the shared marker-write/marker-read logic out of `normalize_pdf.py` /
   `normalize_ticket.py` into a common utility as part of this subtask.
