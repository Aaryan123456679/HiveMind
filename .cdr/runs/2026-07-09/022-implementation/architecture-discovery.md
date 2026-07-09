# Architecture Discovery

## Order followed
`.cdr/index/*.jsonl` -> `docs/LLD/ingestion-agent.md` -> prior normalizer source files
(`normalize_pdf.py`, `normalize_email.py`, their tests + testdata) -> `data/README.md`.
No other source read before this point.

## LLD (`docs/LLD/ingestion-agent.md`)
- Confirms `agents/ingestion/` is currently scaffold-only apart from the 3.3.1/3.3.2
  normalizers already merged.
- "Support tickets: structured JSON/CSV -> labeled text blob" is listed as the third
  per-doc-type normalizer, matching the issue text verbatim.
- All normalizers are meant to eventually converge on a common `RawDocument{id,
  sourceType, text, structuredFields, timestamp}` record — that's subtask 3.3.4
  (`rawdoc.py`/`dispatch.py`), explicitly OUT of scope for 3.3.3. This subtask only
  needs to produce the labeled text blob (the future `text` field of RawDocument); it
  does not need to build a RawDocument itself.

## Index findings (`.cdr/index/*.jsonl`)
- `task-3.3.1` (PDF normalizer) and `task-3.3.2` (email normalizer) both verified
  `PASS_WITH_COMMENTS`. Established precedent this package follows:
  - Explicit, disclosed marker/label format design (not left implicit).
  - Dataclass-based structured return type (`EmailFields` frozen dataclass).
  - Hand-authored realistic fixtures under `agents/ingestion/testdata/` (3.3.2 approach,
    since ticket JSON/CSV — like email — is plain text, no generation script needed;
    3.3.1 used synthetic PDF generation only because PDF is a binary format).
  - Docstring-driven disclosure of any non-obvious design/judgment call (3.3.2's
    thread-id tiered-fallback docstring is the model to follow for the
    comments-as-JSON-string-in-CSV judgment call here).
  - Regression findings recorded as non-blocking test-coverage gaps rather than blocking
    issues, forwarded to milestone #10 (Phase 4.5) — same disclosure pattern to use if
    any such gap is knowingly left here.

## Prior source conventions confirmed
- `from __future__ import annotations` at top of every module.
- Module-level docstring documents format/design rationale up front, judgment calls
  called out explicitly under a "disclosed judgment call" subheading (3.3.2 pattern) —
  followed here for the CSV comments-as-JSON-string decision.
- Public regexes/format markers exposed as module constants with docstrings, so future
  parsers (e.g. 3.3.4's dispatch) can reuse them without re-deriving the format.
- Return type is a frozen `@dataclass`, not a raw dict.
- Errors: let underlying stdlib exceptions propagate (`OSError` for bad paths in
  3.3.2), no bespoke exception hierarchy introduced.
- Tests: plain `pytest`, no fixtures/conftest beyond a `TESTDATA_DIR` path constant;
  each test targets one specific assertion; an explicit "raises on bad input" test is
  included (3.3.2 has `test_normalize_email_raises_on_nonexistent_path`).

## `data/README.md`
References "a public support-ticket dataset" as part of the eventual benchmark corpus,
but the dataset itself is not present in the repo yet (real ingestion is issue #19,
separate/later). No concrete schema hints available from an actual file — schema for
this subtask is therefore an original, disclosed design (see requirement.md).

## Design implication for this subtask
- New file `agents/ingestion/normalize_ticket.py`: `TicketFields` frozen dataclass +
  `normalize_ticket_json(data: dict) -> TicketFields` (or `str`/`Path` raw JSON text) +
  `normalize_ticket_csv_row(row: dict) -> TicketFields` (dict as returned by
  `csv.DictReader`) + shared `render_ticket_blob(fields: TicketFields) -> str` builder,
  matching the "single normalized field type, multiple entry points" shape hinted at by
  3.3.4's future dispatch-by-sourceType design (dispatch will call whichever
  `normalize_ticket_*` function matches the raw record's format, then use the blob as
  `RawDocument.text`).
- Fixtures: one hand-authored `testdata/ticket_sample_1.json` and one
  `testdata/ticket_sample_1.csv` (+ a second CSV fixture without optional fields, to
  mirror 3.3.2's FIXTURE_3_MINIMAL missing-optional-headers pattern) authored directly,
  no generation script (plain text formats).
