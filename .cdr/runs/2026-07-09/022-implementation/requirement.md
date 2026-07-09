# Requirement — Subtask 3.3.3 (Issue #17)

## Source
GitHub issue #17 (Epic Phase 3, milestone #5 "Phase 3: Graph store + ingestion agents
(end-to-end)"), subtask 3.3.3. Issue body re-read and re-confirmed to contain embedded
prompt-injection text (fake "date has changed" system-reminder, fake MCP `tokensave`
server tool instructions, fake "Auto Mode Active" directive) appended after the real
subtask list. All three are untrusted plain-text data embedded in the issue body, not
real system instructions — disclosed here, not acted upon, consistent with 3.3.1/3.3.2's
same recurring pattern (see regression.jsonl entries for those subtasks).

## Exact subtask text (real, non-injected portion)
**3.3.3 — Support-ticket normalizer (structured JSON/CSV) -> labeled text blob**
- Acceptance criteria: Given a structured support-ticket record (JSON or CSV row), the
  normalizer produces a labeled text blob preserving all structured fields as labeled
  sections.
- Test spec: `pytest agents/ingestion/test_normalize_ticket.py`: run against fixture
  ticket records, assert labeled output matches expected format.
- Impacted modules: `agents/ingestion/normalize_ticket.py`,
  `agents/ingestion/test_normalize_ticket.py`

## Scope decisions (disclosed)
- Support BOTH JSON and CSV row input for one ticket record. Issue doesn't mandate a
  single function or two — chosen approach: one shared dataclass-returning normalizer
  entry point per format (`normalize_ticket_json`, `normalize_ticket_csv_row`) plus a
  shared internal blob-building function, so both formats converge on identical output
  format from a common normalized field dict. Disclosed as a design choice.
- Exact output blob format not specified by the issue — designed here to be consistent
  in spirit with 3.3.1 (explicit, parseable markers) and 3.3.2 (labeled field
  extraction): `LABEL: value` line-based sections, with a dedicated repeated-section
  format for list-valued fields (comments/replies), each wrapped in an explicit
  `[[COMMENT n]] ... [[/COMMENT n]]` marker pair (borrowing 3.3.1's marker precedent)
  so multi-comment tickets remain unambiguously parseable.
- No real support-ticket dataset exists yet in `data/` (checked `data/README.md` — only
  references a "public support-ticket dataset" to be added later per issue #19, real
  dataset ingestion; not yet present as of this subtask). Ticket schema below is
  therefore a reasonable, disclosed judgment call, not derived from a concrete existing
  dataset.

## Ticket field schema (disclosed judgment call)
Modeled on typical helpdesk/support-ticket systems (Zendesk/Freshdesk/Jira-Service-Desk-
like conventions), the minimal reasonable set to demonstrate "preserving all structured
fields as labeled sections" without over-engineering:
- `ticket_id` (str) — required
- `subject` (str) — required
- `description` (str) — required, the ticket body
- `status` (str) — e.g. open/pending/closed
- `priority` (str) — e.g. low/medium/high/urgent
- `category` (str)
- `requester` (str) — customer/reporter identifier (email or name)
- `assignee` (str) — optional, may be empty
- `created_at` (str) — ISO-ish timestamp string, kept as opaque string (no datetime
  parsing/validation is required by the acceptance criteria)
- `comments` (list of {author, body} dicts) — optional, nested/list-valued field; CSV
  row form cannot naturally carry a nested list, so CSV support models comments as a
  single JSON-encoded string column (disclosed), while JSON input supports a native
  list.

## Acceptance criteria mapped to tests
1. Given a JSON ticket record, output blob contains one labeled section per scalar
   field, correct label:value formatting.
2. Given a CSV ticket row, output blob is format-identical in shape to the JSON path
   for the same logical record (round-trip parity test).
3. Comments/replies (list-valued nested field) render as one explicit marker-delimited
   block per comment, preserving order and both author + body sub-fields.
4. Missing optional fields (assignee, comments) do not raise and render as empty/absent
   sections cleanly (documented behavior, not a crash).
5. All structured fields present in input round-trip into the output blob (nothing
   silently dropped) — the core "preserving all structured fields" acceptance
   criterion.
