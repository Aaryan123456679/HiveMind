# Requirement — subtask 4.5.15.2 (issue #53, milestone #10)

## Source
`gh issue view 53` (fetched fresh this run). Epic: Phase 4.5 storage-engine
technical-debt follow-ups, `agents/ingestion/` + `data/` modules. Issue #53
subtask list (6 total, this run implements #2 of 6):

- 4.5.15.1 — DONE + verified (2026-07-11-093-implementation /
  095-verification, commit 974926d). Documented the LEN=k trust boundary in
  `normalize_pdf.py`'s module + `iter_pages()` docstrings, added 3 regression
  tests to `agents/ingestion/test_normalize_pdf.py` (`test_len_trust_boundary_*`).
- **4.5.15.2 (this run)** — Add Unicode round-trip regression test for
  `normalize_pdf`/`iter_pages`.
  - Acceptance criteria: a test exercises
    `iter_pages(_page_marker(1, unicode_text))` with non-ASCII/multi-byte text
    (accented characters, CJK, emoji/astral-plane code points) and asserts
    exact round-trip fidelity.
  - Test spec: `pytest agents/ingestion/test_normalize_pdf.py -k unicode_round_trip`.
  - Impacted modules: `agents/ingestion/test_normalize_pdf.py` (test-only,
    per issue text — no production-code module listed).
- 4.5.15.3 — normalize_email fixture coverage (multipart, quoted-printable,
  display-name-From, multi-address-Cc). Files: `agents/ingestion/testdata/`,
  `agents/ingestion/test_normalize_email.py`. No overlap with 4.5.15.2.
- 4.5.15.4 — Soften normalize_ticket.py's docstring overclaim about
  "reliably parse-able" comment-block format. Doc-only, no test required.
  File: `agents/ingestion/normalize_ticket.py`. No overlap with 4.5.15.2.
- 4.5.15.5 — `normalize_pdf` returns `page_count` directly instead of
  `dispatch_pdf` recomputing via a second `iter_pages` pass. Files:
  `agents/ingestion/normalize_pdf.py`, `agents/ingestion/dispatch.py`,
  `agents/ingestion/test_dispatch.py`. **File-overlap risk**: touches
  `normalize_pdf.py` (production code), same file 4.5.15.1 touched (docstrings
  only) — 4.5.15.2 does NOT touch `normalize_pdf.py` at all, so no conflict
  between 4.5.15.2 and 4.5.15.5. 4.5.15.5 changes `normalize_pdf`'s return
  type/shape (likely a dataclass/tuple with page_count), so it should be
  sequenced carefully against 4.5.15.1's already-landed docstring edits (no
  code collision expected, both touch different regions) and must be
  implemented AFTER re-reading normalize_pdf.py fresh at that time.
- 4.5.15.6 — data loader docstring fix + strengthen ID-stability test.
  Files: `data/load_bitext.py`, `data/load_enron.py`, `data/test_loaders.py`.
  No overlap with 4.5.15.2 or `agents/ingestion/`.

## Disjointness check vs 4.5.15.1 (mandated by dispatcher)
4.5.15.1's committed diff (974926d) touched:
  - `agents/ingestion/normalize_pdf.py` — docstring-only edits (module
    docstring + `iter_pages()` docstring), no behavior change.
  - `agents/ingestion/test_normalize_pdf.py` — appended 3 new tests under a
    new `# --- F3 (task-3.3.1): LEN=k trust-boundary documentation follow-up ---`
    section (lines 159-201 in the current committed file).

4.5.15.2 touches only `agents/ingestion/test_normalize_pdf.py` (test-only,
per issue text — does not modify `normalize_pdf.py`). This is the SAME file
4.5.15.1 modified (test file, not the production file), so there is a
same-file (not same-region) overlap. Verified by reading the file fresh
post-974926d (working tree clean for both files, `git status --short
agents/ingestion/` empty before this run) — the new unicode round-trip test
will be appended after 4.5.15.1's last test (`test_len_trust_boundary_len_exceeding_remaining_text_raises_value_error`,
ending line 201), in a new section, so no line-range collision with
4.5.15.1's added tests. Proceeding sequentially (fresh read, append-only) per
dispatcher instruction rather than assuming blind independence.

## Acceptance criteria (restated, precise)
1. New test(s) in `agents/ingestion/test_normalize_pdf.py` that:
   - Build page text containing accented Latin characters, CJK characters,
     and an emoji/astral-plane code point (surrogate-pair-requiring in
     UTF-16, i.e. code point > 0xFFFF).
   - Round-trip through `_page_marker(1, unicode_text)` -> `iter_pages(...)`.
   - Assert the recovered text is byte-for-byte/char-for-char identical to
     the original (accounting for `_page_marker`'s trailing-newline
     normalization, matching existing test conventions).
2. Test name matched by `-k unicode_round_trip` (per test spec).
3. No modification to `normalize_pdf.py` production code (issue lists only
   the test file as impacted module for this subtask).
