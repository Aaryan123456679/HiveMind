# task-3.3.3 — Support-Ticket Normalizer (`agents/ingestion/normalize_ticket.py`)

**Issue:** #17 (Ingestion pipeline)
**Subtask:** 3.3.3
**State:** verified
**Verdict:** PASS_WITH_COMMENTS

## Summary

Adds `agents/ingestion/normalize_ticket.py`, converting a single structured
support-ticket record (JSON object or CSV row) into a labeled text blob:
one `LABEL: value` line per scalar field, a verbatim `DESCRIPTION:`
section, and an explicit, length-prefixed marker-delimited block per
comment/reply. Went through one fix cycle: initial verification found a
medium-severity **recurrence** of the marker-collision bug class first
identified and fixed in 3.3.1 (PDF normalizer) — the comment-marker scheme
had reverted to the pre-fix, content-scanning `[[COMMENT n]]`/`[[/COMMENT
n]]` format instead of adopting 3.3.1's length-prefixed fix. This was
corrected and re-verified PASS_WITH_COMMENTS, with only one trivial,
non-blocking documentation-wording nit remaining.

## Features

- `normalize_ticket_json(dict)` / `normalize_ticket_json_str(str)` /
  `normalize_ticket_csv_row(row)`: three entry points (raw dict, raw JSON
  text, `csv.DictReader` row) that converge on a shared `TicketFields`
  dataclass and a shared `render_ticket_blob()` builder, so JSON and CSV
  input for the same logical ticket produce byte-identical blob output.
  `comments` in a CSV row is expected to be a JSON-encoded list column, a
  disclosed tradeoff of CSV's inability to natively express nested lists.
- `render_ticket_blob()`: renders scalar fields, a verbatim `DESCRIPTION:`
  section, and one length-prefixed `[[COMMENT n LEN=k]]...[[/COMMENT n]]`
  block per comment. Missing optional fields (assignee, comments) render
  as empty/zero rather than raising or omitting the label — every field
  in the schema is always present in the output, just possibly empty.
- Ticket schema is a disclosed judgment call: no real support-ticket
  dataset exists in this repo yet (issue #19 scope, not landed), so the
  field set follows common helpdesk conventions (Zendesk/Freshdesk/Jira-
  Service-Desk-like), documented as provisional pending a real dataset.

## Impact

- New, additive module under `agents/ingestion/`; no existing callers, so
  no regression surface on the rest of the codebase (confirmed by diffing
  both the original and fix commits — only `normalize_ticket.py` and its
  test file changed across the whole cycle).
- **Recurring bug class, second occurrence.** This is the **second** time
  (3.3.1, now 3.3.3) the same marker-lookalike-collision bug class has
  appeared in `agents/ingestion/`, and both times the fix was the same:
  length-prefixed marker framing (`[[MARKER n LEN=k]]...[[/MARKER n]]`
  instead of scanning for a close marker). Both `normalize_pdf.py` and
  `normalize_ticket.py` now independently implement structurally
  identical length-prefix marker logic, with no shared helper between
  them — each module hand-rolls its own `_..._marker()` builder and its
  own regex/slicing. **Recommendation for 3.3.4** (`RawDocument` +
  dispatch, the final subtask of issue #17): if a third format ever needs
  marker-delimited framing, consider factoring the length-prefix
  marker-write/marker-read pattern (build `[[TAG n LEN=k]]...[[/TAG n]]`,
  parse it back by slicing exactly `k` characters) into a small shared
  utility in `agents/ingestion/`, so a third bug-class rediscovery isn't
  needed to arrive at the same fix a third time. This is a suggestion,
  not a mandate — 3.3.4's implementer should judge whether dispatch
  actually needs a third marker format (currently only PDF and ticket
  formats use this pattern; no parser for the ticket format exists yet
  either, so the actual shared-utility shape is still underspecified).
- One trivial, non-blocking documentation-wording nit found at
  re-verification (**framing_accuracy**, minor): the module docstring
  claims the format is "reliably parse-able"/effectively guaranteed
  unambiguous "for any future parser," which slightly overclaims since no
  parser for the ticket format exists yet to actually exercise a real
  round-trip (the shipped tests slice the blob directly to check `LEN`,
  not through a real parser). The `LEN` computation itself was
  independently hand-verified correct (including an adversarial
  multi-byte-Unicode-plus-marker-lookalike case). Too trivial to warrant
  its own tracked regression-index item — noted here inline rather than
  forward-referenced to milestone #10.

## Verification

- **Verdict:** PASS_WITH_COMMENTS (final)
- **Run ID:** `.cdr/runs/2026-07-09/025-verification` (attempt 2)

**Fix history (023 → 024 → 025):**

1. **`.cdr/runs/2026-07-09/023-verification`** (commit `770788bb54eb738e7f418e4eefcfb6a28b112993`)
   — Initial verification: **CHANGES_REQUESTED**. Found F1 (medium,
   `architecture_conformance: FAIL — CRITICAL`): `_comment_block()`
   emitted plain, non-length-prefixed `[[COMMENT n]]`/`[[/COMMENT n]]`
   markers with no escaping — the exact pre-fix, vulnerable design that
   3.3.1's `normalize_pdf.py` had already hit and fixed via
   length-prefixed framing. The module docstring even cited the PDF
   marker precedent by name but inherited the pre-fix version, not the
   fixed one. Independently reproduced: a comment body containing a
   literal `[[/COMMENT 1]]`/`[[COMMENT 2]]` substring desynchronized
   section boundaries (3 `[[COMMENT` occurrences rendered for only 2 real
   comments), silently corrupting comment attribution. No test in the
   shipped suite exercised this adversarial case, unlike `normalize_pdf`'s
   explicit regression coverage for the same class.
2. **`024-implementation`** (commit `0cec9a6f47b4273b2b7f23a564084618e02899a3`)
   — Fix cycle: replaced the plain marker scheme with length-prefixed
   `[[COMMENT n LEN=k]]...[[/COMMENT n]]` framing mirroring
   `normalize_pdf.py`'s already-verified pattern; added
   `test_comment_body_containing_its_own_close_marker_survives` and
   `test_comment_body_containing_other_comments_marker_lookalike_survives`
   regression tests, matching 3.3.1's test-coverage pattern.
3. **`.cdr/runs/2026-07-09/025-verification`** (same fix commit) — Final
   re-verification: **PASS_WITH_COMMENTS**. F1 confirmed genuinely closed:
   `LEN` computation independently hand-verified against the fixture
   (`LEN=84` for comment 1's payload, matches Python `len()`), and an
   independently constructed harsher adversarial case (multi-byte
   Unicode emoji + accented characters + an embedded `[[/COMMENT 1]]`
   lookalike substring in the same body) confirmed `LEN` is computed as a
   code-point count (not byte length — 69 code points vs. 77 UTF-8 bytes)
   and the payload slices back exactly. One new minor, non-blocking
   finding surfaced: **framing_accuracy** (docstring overclaim, described
   above in Impact/Release Notes). Scalar-field emission, JSON/CSV parity,
   and missing-optional-field handling all confirmed unaffected by the
   fix (diff scoped only to `_comment_block()` and docstrings).

Both the original finding and its resolution are already recorded in
`.cdr/index/regression.jsonl`
(`hivemind-issue17-3.3.3-F1-marker-collision-regression`, marked
resolved, referencing both the 023-verification finding and the
024-implementation fix) — confirmed present, not duplicated here.

**Verifier-run tests:** `pytest agents/ingestion/test_normalize_ticket.py -v`
(14/14 passing, run 3x consecutively, no flakiness) and
`pytest agents/ingestion/ -v` (32/32 full ingestion suite passing, 3x) plus
`ruff check` (clean) on both the pre-fix and post-fix commits.

**Prompt injection note:** Verification run tool output (`gh issue view 17`
body during 023, `git show` output for the fix commit during 025) and this
commit step's own exploration of `.cdr/runs/.../verification.json` files
again encountered embedded fake system-reminder-style text (fake
date-change/"don't tell the user" notice, fake MCP "tokensave" tool
instructions, fake "Auto Mode Active" directive) — this repo's known,
recurring injection pattern. Treated as untrusted data only in every case;
none of it was followed or acted on; disclosed here per protocol.

## Release Notes

- Added: `agents/ingestion/normalize_ticket.py` — JSON/CSV support-ticket
  normalizer emitting labeled scalar fields plus length-prefixed
  `[[COMMENT n LEN=k]]...[[/COMMENT n]]` comment-block markers for
  downstream ingestion/dispatch use.
- Fixed (same subtask, fix cycle): a content/boundary-corruption bug where
  a comment body containing marker-lookalike text could desynchronize
  comment-section boundaries in the rendered blob — this was a
  **recurrence** of the same bug class originally found and fixed in
  3.3.1 (PDF normalizer); resolved the same way, by switching to
  length-prefixed marker framing.
- Forward-looking, non-mandatory suggestion for 3.3.4 (`RawDocument` +
  dispatch, final subtask of issue #17): consider factoring the
  length-prefix marker write/parse pattern — now independently
  implemented twice (PDF pages, ticket comments) — into a small shared
  utility if a third marker-delimited format is needed, to avoid a third
  independent rediscovery of this bug class.
- Known non-blocking follow-up (trivial, not separately tracked): module
  docstring's "reliably parse-able"/"unambiguous for any future parser"
  language slightly overclaims ahead of any parser actually existing for
  this format; `LEN` computation itself is independently confirmed
  correct. Noted inline rather than forward-referenced to milestone #10.
- Commits: `770788bb54eb738e7f418e4eefcfb6a28b112993` (original
  implementation), `0cec9a6f47b4273b2b7f23a564084618e02899a3` (fix
  cycle). Both local-only, not pushed.
