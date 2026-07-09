# task-3.3.4 — RawDocument record + normalizer dispatch (final subtask, issue #17)

**Issue:** #17 (Ingestion pipeline)
**Subtask:** 3.3.4
**State:** verified
**Verdict:** PASS_WITH_COMMENTS

## Summary

Fourth and **final** subtask of GitHub issue #17 ("[3] Per-doc-type
normalization", `agents/ingestion/`). Adds `agents/ingestion/rawdoc.py`
(the common `RawDocument` record type) and `agents/ingestion/dispatch.py`
(a single `dispatch()` entry point that routes to the three already-verified
per-doc-type normalizers — PDF (3.3.1), email (3.3.2), support ticket
(3.3.3) — by `source_type`), giving the not-yet-built segmentation agent
(`docs/LLD/ingestion-agent.md`) one consistent hand-off shape regardless of
source document type. Independently re-verified **PASS_WITH_COMMENTS**, no
fix cycle needed. Purely additive: 2 new modules + 1 new test file; none of
the three existing normalizers were modified.

## Features

- `RawDocument` (`rawdoc.py`): frozen dataclass with `id`, `source_type`,
  `text`, `structured_fields`, `timestamp`. Field names are disclosed,
  deliberate snake_case (not the issue text's literal `sourceType`/
  `structuredFields` camelCase), matching the pre-existing package
  convention already established by `EmailFields`/`TicketFields`. `id` has
  no default (omitting it is a `TypeError`, not a silent empty string);
  `timestamp` defaults to a genuinely UTC-aware value when not supplied.
- `dispatch()` (`dispatch.py`): single literal-spec-satisfying entry point
  selecting the correct normalizer by `source_type` and returning a
  `RawDocument`. Backed by three convenience wrappers —
  `dispatch_pdf`, `dispatch_email`, `dispatch_ticket_json` /
  `dispatch_ticket_csv_row` — which `dispatch()` delegates to.
- Strict argument validation: each source type requires exactly the kwargs
  it needs (e.g. `path` for pdf, exactly one of `data`/`row` for ticket) and
  raises a specific `ValueError` otherwise; an unknown `source_type` raises
  a `ValueError` listing the valid options.
- `structured_fields` mapping is source-type-specific and deliberately
  excludes ticket comment bodies (already fully represented in the rendered
  `text` via 3.3.3's length-prefixed `[[COMMENT n LEN=k]]` markers), avoiding
  dual-representation drift between `text` and `structured_fields`.
- `_ticket_structured_fields` helper avoids duplicating the scalar-field
  list across the ticket-JSON and ticket-CSV wrapper paths.
- Zero changes to `normalize_pdf.py`, `normalize_email.py`, or
  `normalize_ticket.py` — 3.3.3's forward-looking suggestion to factor a
  shared marker-framing utility was deliberately not taken up here (disclosed
  judgment call, see Impact), since dispatch itself needs no marker framing
  of its own.

## Impact

- New files only: `agents/ingestion/rawdoc.py`, `agents/ingestion/dispatch.py`,
  `agents/ingestion/test_dispatch.py`. No existing files touched — zero
  regression risk to the three already-verified normalizers, confirmed via
  `git diff` showing them byte-identical across this commit.
- Gives the future segmentation agent one consistent `RawDocument` shape to
  consume regardless of whether the source was a PDF, an Enron-format email,
  or a JSON/CSV support ticket.
- Disclosed, deliberate scope decision: the shared marker-write/marker-read
  utility suggested at 3.3.3's close-out (factoring the length-prefixed
  `[[TAG n LEN=k]]...[[/TAG n]]` pattern now independently implemented twice,
  in `normalize_pdf.py` and `normalize_ticket.py`) was **not** built here.
  `dispatch.py` doesn't itself need marker framing, and no third
  marker-delimited format exists yet, so the suggestion's own precondition
  ("if a third format ever needs it") was correctly judged not to have been
  met. This is a non-blocking, disclosed decision, not an oversight — see the
  Issue #17 closure summary below for the consolidated status of that
  suggestion.
- One new non-blocking finding: `dispatch_pdf` recomputes `page_count` via a
  second pass over the already-parsed marker text (`iter_pages`) rather than
  having `normalize_pdf` return the count directly alongside the text.
  Negligible perf cost at this scale; forward-referenced to milestone #10
  below.

## Verification

- **Verdict:** PASS_WITH_COMMENTS (no fix cycle required)
- **Run ID:** `.cdr/runs/2026-07-09/028-verification`
- **Commit reviewed:** `0b541d00cb6ce125d333fddd8fa7c1edbd1274d1`
- All 9 verification dimensions independently checked and passed:
  `requirements_conformance` (PASS — snake_case renaming confirmed as a
  genuine pre-existing package convention, not a post-hoc excuse, by reading
  `EmailFields`/`TicketFields`), `architecture_conformance` (PASS —
  frozen-dataclass hand-off DTO matches `docs/LLD/ingestion-agent.md`),
  `regression_risk` (PASS — `git diff` confirms the 3 existing normalizers
  are byte-identical; independently re-dispatched a ticket through both
  JSON and CSV paths and confirmed 3.3.3's length-prefixed comment markers
  are preserved with no regression), `edge_cases_and_validation` (PASS — 6
  independently-constructed adversarial dispatch calls beyond the shipped
  suite all raised the correct, specific `ValueError`s; `id`-required and
  UTC-aware-`timestamp` behavior independently confirmed), `security`
  (PASS — no new attack surface, no eval/exec, plain JSON-serializable
  scalar output), `performance` (PASS, with the non-blocking page_count
  finding noted above), `maintainability` (PASS), `test_coverage` (PASS —
  `pytest agents/ingestion/ -v` run 3x independently: 54/54 passing, zero
  flakiness; `ruff check` clean on all 3 new files), `scope_containment`
  (PASS — `git show --name-only` confirms only the 3 declared files
  changed), and `issue_closure` (**PASS_WITH_COMMENT** — cross-checked the
  full issue #17 body against all 4 subtasks; no scope beyond the 4 listed
  subtasks found; issue is genuinely feature-complete against its written
  text — see closure summary below).
- Zero must-fix findings. The one non-blocking finding is the `page_count`
  double-computation described above.
- Verification also independently confirmed a prompt-injection attempt:
  `git show 0b541d0` tool output contained fake embedded system-reminder-style
  text (a fake "date changed, don't tell the user" notice, a fake MCP
  "tokensave" tool-instructions block, and a fake "Auto Mode Active"
  directive) appended after the legitimate commit diff — consistent with
  this repo's known, recurring injection pattern. Treated as untrusted data
  only; not followed, not acted upon; disclosed here per protocol. This same
  pattern recurred during this commit-documentation step's own tool output
  (bash command output) and was likewise treated as untrusted data, not
  followed, and is disclosed here.

## Release Notes

- Added `agents/ingestion/rawdoc.py` (`RawDocument`) and
  `agents/ingestion/dispatch.py` (`dispatch()` + per-source-type
  convenience wrappers): a single normalized hand-off record and entry
  point across the PDF, email, and support-ticket normalizers, for
  consumption by the future segmentation agent.
- No existing normalizer code changed; purely additive.
- Known non-blocking follow-up (not fixed now): `dispatch_pdf` recomputes
  `page_count` via a redundant second pass over already-parsed marker text
  instead of `normalize_pdf` returning it directly. Forward-referenced to
  GitHub milestone #10 (issues #38-42).
- Commit: `0b541d00cb6ce125d333fddd8fa7c1edbd1274d1`. Local-only, not pushed.

---

## Issue #17 closure summary (Per-doc-type normalization, `agents/ingestion/`)

All 4 subtasks under issue #17 are now implemented and independently
verified:

1. **3.3.1 — PDF normalizer** (`agents/ingestion/normalize_pdf.py`,
   implementation commits `73310f1da5d01ab632c7b9cf5a5ad311b0ade51e` /
   `462b1831b795788965380bfc6b3201821e4b95ca`, fix-cycle commits
   `259f9b85d9e3ebf07200071e3c54345bdcb36ed7` /
   `360496321433e14209af44956cccdcdb74a7004d`, close-out commit
   `d0b3d5575d136aaa35acbcc9f31dc183f00a7775`): pymupdf-based PDF-to-text
   normalizer with page-boundary markers. Went through one fix cycle: a
   medium-severity content-truncation bug in the original content-scanning
   marker format (F1) was fixed by switching to the length-prefixed
   `[[PAGE n LEN=k]]...[[/PAGE n]]` framing that all later marker-bearing
   formats in this issue inherited. Verified **PASS_WITH_COMMENTS**.
2. **3.3.2 — Enron email normalizer** (`agents/ingestion/normalize_email.py`,
   commit `4bdfb31f12d9a5bd2f541a190ac1494e0e37166d`, close-out commit
   `1d29544`): stdlib-only Enron-corpus email parser into a common
   `EmailFields` shape, with a disclosed tiered thread-id fallback since the
   corpus has no native thread-id header. Verified **PASS_WITH_COMMENTS**
   (non-blocking test-fixture coverage gap for multipart/quoted-printable/
   display-name-From/multi-address-Cc messages; code independently confirmed
   correct today).
3. **3.3.3 — Support-ticket normalizer**
   (`agents/ingestion/normalize_ticket.py`, implementation commit
   `770788bb54eb738e7f418e4eefcfb6a28b112993`, fix-cycle commit
   `0cec9a6f47b4273b2b7f23a564084618e02899a3`, close-out commit
   `bd492c021f6df37fe82e9702d7d6e0a185f74f70`): JSON/CSV support-ticket
   normalizer. Went through one fix cycle: a **recurrence** of 3.3.1's exact
   marker-collision bug class (comment markers reverted to a non-length-
   prefixed, content-scanning scheme) was caught and fixed the same way, via
   the same length-prefixed marker pattern. Verified **PASS_WITH_COMMENTS**
   (trivial non-blocking docstring-overclaim finding).
4. **3.3.4 — RawDocument + dispatch** (`agents/ingestion/rawdoc.py`,
   `agents/ingestion/dispatch.py`, implementation commit
   `0b541d00cb6ce125d333fddd8fa7c1edbd1274d1`, this record): common
   hand-off record and single dispatch entry point across all three
   normalizers, closing out the issue. Verified **PASS_WITH_COMMENTS** (no
   fix cycle needed), consolidated above.

**Net result**: a working, internally-consistent per-doc-type normalization
pipeline — PDF (3.3.1), Enron email (3.3.2), and JSON/CSV support ticket
(3.3.3) normalizers, unified behind a single `RawDocument` + `dispatch()`
hand-off contract (3.3.4) — ready for the not-yet-built segmentation agent to
consume. All implementation and fix-cycle commits listed above are
local-only and have not been pushed.

### Consolidated non-blocking follow-ups (all forward-referenced to milestone #10, issues #38-42)

The following findings were surfaced across the four subtasks' verification
passes. None were blocking; all are already recorded in
`.cdr/memory/pending.md` and `.cdr/index/regression.jsonl`, and are
consolidated here for issue-level visibility:

- **F3 (3.3.1, low)** — `normalize_pdf.py`'s length-prefixed `LEN=k` marker
  framing trusts its own embedded length without independent cross-
  validation against actual page-text length (residual trust boundary, not
  a reproduced bug). `.cdr/index/regression.jsonl`:
  `hivemind-issue17-3.3.1-F3-len-trust-boundary`.
- **F4 (3.3.1, low)** — no automated Unicode round-trip test for
  `normalize_pdf`/`_page_marker`/`iter_pages` (accented/CJK/emoji text).
  Hand-verified correct, but untested in CI. `.cdr/index/regression.jsonl`:
  `hivemind-issue17-3.3.1-F4-missing-unicode-test`.
- **Test-coverage gap (3.3.2, low)** — none of `normalize_email.py`'s 3
  shipped fixtures exercise multipart bodies, quoted-printable/other
  transfer encodings, display-name `From` headers, or multi-address Cc
  lists, even though the implementation independently confirmed correct on
  all of them. `.cdr/index/regression.jsonl`: `17-3.3.2-email-normalizer`
  entry (020-verification).
- **Marker-collision bug class, resolved twice (3.3.1 then 3.3.3)** — not an
  open follow-up (both occurrences were fixed within their own subtask's fix
  cycle), but worth restating at issue level: the same
  length-prefix-marker-collision bug class was independently discovered and
  fixed in both `normalize_pdf.py` (3.3.1, commit `259f9b85d`) and
  `normalize_ticket.py` (3.3.3, commit `0cec9a6f4`), each time via the
  identical `[[TAG n LEN=k]]...[[/TAG n]]` fix pattern, with no shared
  helper between them. 3.3.3's verification suggested factoring this into a
  shared utility in `agents/ingestion/` if a third marker-delimited format
  ever appears. 3.3.4 deliberately did not build that utility (see Impact
  above) since dispatch needs no marker framing of its own and no third
  format exists yet — the suggestion's precondition was not met. Left as a
  standing, non-mandatory suggestion for whichever future subtask
  introduces a third marker-delimited format.
- **Docstring overclaim (3.3.3, trivial, not separately tracked)** —
  `normalize_ticket.py`'s module docstring claims the comment-marker format
  is "reliably parse-able"/"unambiguous for any future parser," which
  slightly overclaims since no real parser for the format exists yet (the
  shipped tests slice the blob directly rather than exercising a real
  round-trip parser). `LEN` computation itself independently confirmed
  correct. Noted inline in 3.3.3's record, forward-referenced here.
- **`page_count` double-computation (3.3.4, minor)** — `dispatch_pdf`
  recomputes `page_count` via a second `iter_pages()` pass instead of
  `normalize_pdf` returning it directly. Negligible cost. Recorded in
  `.cdr/index/regression.jsonl` (028-verification entry).

### Explicit status: issue #17 is READY TO CLOSE but has NOT been closed

All 4 subtasks (3.3.1, 3.3.2, 3.3.3, 3.3.4) are now implemented, independently
verified (PASS_WITH_COMMENTS on all four), and committed locally. Per the
028-verification `issue_closure` dimension, the full text of GitHub issue #17
was cross-checked against all 4 subtasks and no additional undelivered scope
was found — the issue is genuinely feature-complete.

**This record does not close or otherwise touch issue #17 on GitHub, and
none of the local commits in this sequence have been pushed.** Per standing
instruction, closing the issue, updating milestone state, or pushing any of
these commits requires **separate, explicit user authorization** that has
not yet been given. This close-out record — like the 3.3.1/3.3.2/3.3.3
records before it — only documents that the work is done and verified; it is
not itself that authorization.
