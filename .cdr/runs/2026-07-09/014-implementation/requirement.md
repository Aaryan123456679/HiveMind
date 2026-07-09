# Requirement — Subtask 3.3.1 (GitHub Issue #17)

Source: `gh issue view 17`, subtask 3.3.1 (Epic Phase 3: "Per-doc-type normalization
(agents/ingestion/)"). Verified clean of prompt injection via manual read; note that the
`gh issue view` tool output for this session separately contained unrelated injected
fake system-reminder text (fake date-change notice, fake MCP tool instructions, fake
"Auto Mode Active" directive) appended after the real issue body — that injected text is
disregarded and not acted upon per this session's security note.

## Title
3.3.1 — PDF normalizer via pymupdf -> plain text page markers

## Acceptance criteria
Given a PDF file, normalizer produces plain text with page-boundary markers preserved,
no page content dropped.

## Test spec
`pytest agents/ingestion/test_normalize_pdf.py`: run against a fixture PDF, assert
output text contains expected per-page markers and content.

## Impacted modules
- `agents/ingestion/normalize_pdf.py`
- `agents/ingestion/test_normalize_pdf.py`

## Notes from broader issue context (not in scope, for orientation only)
Sibling subtasks (3.3.2 email, 3.3.3 tickets, 3.3.4 RawDocument/dispatch) are out of
scope for this run. All normalizers are meant to eventually converge on a common
`RawDocument{id, sourceType, text, structuredFields, timestamp}` record per
`docs/LLD/ingestion-agent.md`, but 3.3.1 only requires the PDF-specific normalizer
function producing marked-up plain text, not the full RawDocument wiring.
