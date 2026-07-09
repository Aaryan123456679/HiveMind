# Plan

1. Define `TicketFields` frozen dataclass: `ticket_id, subject, description, status,
   priority, category, requester, assignee, created_at, comments` where `comments` is
   `tuple[TicketComment, ...]` and `TicketComment` is a small frozen dataclass
   `{author, body}`.
2. Implement `render_ticket_blob(fields: TicketFields) -> str`: shared blob builder.
   Format:
   ```
   TICKET_ID: <id>
   SUBJECT: <subject>
   STATUS: <status>
   PRIORITY: <priority>
   CATEGORY: <category>
   REQUESTER: <requester>
   ASSIGNEE: <assignee>
   CREATED_AT: <created_at>
   DESCRIPTION:
   <description text, verbatim, possibly multi-line>
   COMMENTS: <n>
   [[COMMENT 1]]
   AUTHOR: <author>
   BODY:
   <body text>
   [[/COMMENT 1]]
   ... (repeated per comment, 1-indexed, in input order)
   ```
   Scalar labeled lines use `LABEL: value` (empty string for missing optional
   fields, never omitted -- so the section always exists, addressing "preserving all
   structured fields"). `DESCRIPTION:`/`BODY:` use a header line + verbatim following
   block (multi-line safe) matching 3.3.1's page-payload-on-its-own-line spirit.
   `COMMENTS: <n>` header always present (n=0 when no comments) so parsers can detect
   comment count without scanning.
3. Implement `normalize_ticket_json(data: dict) -> TicketFields`: reads a parsed JSON
   dict (native Python dict/list, not raw text -- keeps JSON-decoding as caller's
   concern, but also provide `normalize_ticket_json_str(text: str)` convenience
   wrapper that does `json.loads` then delegates, for direct fixture-file use in
   tests/dispatch). Missing optional keys default to `""`/empty tuple.
4. Implement `normalize_ticket_csv_row(row: dict) -> TicketFields`: reads a single
   `csv.DictReader`-shaped row dict. `comments` column, if present and non-empty, is
   `json.loads`'d as a list of `{author, body}` dicts; if absent/empty, `()`.
5. Both entry points converge on the same `TicketFields` -> same `render_ticket_blob`
   output for logically-equivalent records (round-trip parity test).
6. Fixtures:
   - `testdata/ticket_sample_1.json`: full record with 2 comments, all fields
     populated.
   - `testdata/ticket_sample_1.csv`: same logical record as the JSON fixture (header +
     1 data row), comments column JSON-encoded, to drive the parity test.
   - `testdata/ticket_sample_2_minimal.csv`: header + 1 row with assignee and comments
     columns empty/absent, to exercise the missing-optional-field path.
7. Tests (`test_normalize_ticket.py`): field extraction from JSON fixture, field
   extraction from CSV fixture, blob format sanity (labeled sections present, in
   order), comments rendered as marker-delimited blocks with correct content id
   count/order, JSON vs CSV parity for the same logical record, minimal-CSV
   optional-field-empty path does not raise and renders empty sections cleanly,
   "preserving all fields" end-to-end assertion (every input field's value appears
   under its label in the output blob).
8. Run `pytest agents/ingestion/test_normalize_ticket.py -v` via `agents/.venv` for
   self-consistency (not verification).
9. Commit locally, write handoff.json with the real final commit hash.
