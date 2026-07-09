# Architecture Discovery

## Reading order followed
1. `.cdr/index/*.jsonl` (feature.jsonl, task.jsonl, regression.jsonl) — prior 3.3.1/3.3.3 decisions,
   the two marker-collision bug-class findings (F1 in each), and 3.3.3's non-mandatory
   shared-marker-utility suggestion for 3.3.4.
2. `docs/LLD/ingestion-agent.md` — confirms `RawDocument{ id, sourceType, text, structuredFields,
   timestamp }` target shape and that `agents/ingestion/` also hosts the (not-yet-built)
   segmentation agent that will consume `RawDocument` downstream — out of scope here, but
   confirms `RawDocument` is meant to be the stable hand-off contract into that stage.
3. `.cdr/commits/task-3.3.3.md` — full narrative of the verification fix cycle on the ticket
   normalizer, including the disclosed judgment calls made there (ticket schema, JSON/CSV
   convergence, length-prefix framing).
4. Read all three normalizer source files directly (`normalize_pdf.py`, `normalize_email.py`,
   `normalize_ticket.py`) plus their test files, since 3.3.4 must adapt their *actual* current
   signatures precisely.

## Key architectural facts established

- Package root for imports is `agents/` (pyproject.toml: `packages = ["ingestion", "query", "llm",
  "eval"]`, `testpaths = ["."]`), so all new code imports as `from ingestion.<module> import ...`
  and tests as `from ingestion.dispatch import ...` — matching existing test files.
- Fixtures live under `agents/ingestion/testdata/`; existing fixtures already cover one instance
  of each source type (`enron_sample_1.txt`, `ticket_sample_1.json`, plus a PDF built at test time
  via `fitz` in `test_normalize_pdf.py` — no committed PDF binary fixture).
- Established code-level naming convention in this exact package is snake_case
  (`EmailFields.thread`, `TicketFields.ticket_id`, `TicketFields.created_at`, etc.) even though
  the issue text and LLD doc use camelCase (`sourceType`, `structuredFields`). The LLD doc's
  camelCase is describing the conceptual/wire shape, not dictating Python identifier casing.
- Return-type shapes to adapt:
  - `normalize_pdf(path) -> str`: no natural id/timestamp/structured fields beyond page count.
  - `normalize_email(path) -> EmailFields(sender, subject, thread, body)`.
  - `normalize_ticket_json(dict) -> TicketFields`, `normalize_ticket_csv_row(row) -> TicketFields`,
    `render_ticket_blob(fields) -> str`.
- Neither PDF nor email normalizer output carries a natural, always-present document id. Ticket
  has `ticket_id` but PDF/email do not have an equivalent. -> `RawDocument.id` must be
  caller-supplied by the dispatch layer, not derived unreliably per source type.
- No normalizer currently produces an ingestion/document timestamp. -> `RawDocument.timestamp`
  is populated at dispatch time (ingestion time), defaulting to `datetime.now(timezone.utc)`,
  overridable by the caller for reproducibility/testing.

## Marker-utility refactor scope decision

3.3.3's close-out note frames the shared marker-utility as relevant "if a third marker format is
needed." This subtask (3.3.4) does not introduce a third length-prefixed marker format: it wraps
already-rendered text (`normalize_pdf` output, `render_ticket_blob` output, `EmailFields.body`)
as an opaque `text` field, and does not need to write or parse a new marker scheme itself. The
condition that motivated the suggestion is not triggered by 3.3.4's actual acceptance criteria.
Extracting the shared helper now would mean touching two already-verified, already-committed
files (`normalize_pdf.py`, `normalize_ticket.py`) with no functional driver in this subtask,
purely for anticipatory de-duplication — judged as scope creep relative to 3.3.4's acceptance
criteria and skipped. Recorded here rather than forced.
