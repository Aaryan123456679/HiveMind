# task-3.3.1 — PDF Normalizer (`agents/ingestion/normalize_pdf.py`)

**Issue:** #17 (Ingestion pipeline)
**Subtask:** 3.3.1
**State:** verified
**Verdict:** PASS_WITH_COMMENTS

## Summary

Adds `agents/ingestion/normalize_pdf.py`, converting a source PDF into plain
text with explicit, machine-parseable page-boundary markers so downstream
segmentation/dispatch code can recover per-page provenance from a flat text
blob. Went through one fix cycle: an initial PASS_WITH_COMMENTS verification
found a medium-severity content-truncation bug in the marker format, which
was fixed and re-verified PASS_WITH_COMMENTS with only low-severity,
non-blocking follow-ups remaining.

## Features

- `normalize_pdf(path)`: opens a PDF via pymupdf (`fitz`), extracts text
  page by page, and returns a single string with every page wrapped in an
  explicit marker pair. Every page — including pages with no extractable
  text — produces exactly one marker pair, so marker-pair count always
  equals source page count.
- `PAGE_MARKER_RE` / `iter_pages()`: exported parser for downstream callers
  (intended consumer: the future `agents/ingestion/dispatch.py`
  `RawDocument` builder) to recover per-page text plus 1-indexed page
  number from the normalized blob.
- Marker format was changed mid-cycle from a content-scanning
  `[[PAGE n]]...[[/PAGE n]]` (backreference-based) scheme to a
  length-prefixed `[[PAGE n LEN=k]]...[[/PAGE n]]` scheme that slices
  exactly `k` characters instead of scanning for a closing marker,
  eliminating the entire class of delimiter-in-payload collisions.
- Resource-safe: `fitz.open()` failures leak nothing (call is outside the
  try block); the open document handle is always closed via `finally`,
  even if per-page extraction raises mid-iteration.
- Test suite (`test_normalize_pdf.py`) builds its PDF fixtures at test time
  via pymupdf itself — no committed binary fixture, no new test-only
  dependency.

## Impact

- New, additive module under `agents/ingestion/`; no existing callers, so
  no regression surface on the rest of the codebase.
- Establishes the page-marker text format that future ingestion/dispatch
  code will depend on for page-level provenance — the fix-cycle bug (F1,
  below) mattered specifically because this format is about to become
  load-bearing for downstream segmentation.
- Two low-severity, non-blocking follow-ups (F3, F4) remain open and are
  forward-referenced to GitHub milestone #10 ("Phase 4.5: technical debt &
  correctness follow-ups", issues #38-42), per this repo's standing
  convention for non-blocking verification findings. Recorded in
  `.cdr/memory/pending.md` and `.cdr/index/regression.jsonl`.

## Verification

- **Verdict:** PASS_WITH_COMMENTS (final)
- **Run ID:** `.cdr/runs/2026-07-09/017-verification` (attempt 2 of 3)

**Fix history (015 → 016 → 017):**

1. **`.cdr/runs/2026-07-09/015-verification`** (commits `73310f1d`, `462b183`)
   — Initial verification: PASS_WITH_COMMENTS. Found **F1** (medium): the
   non-greedy backreference `PAGE_MARKER_RE` silently truncated a page's
   captured text if that page's own extracted content contained a literal
   substring matching its own close marker (e.g. `[[/PAGE 1]]` appearing
   verbatim inside page 1's text) — plausible for a PDF that itself
   documents this marker syntax. Marker-pair count stayed correct, masking
   the corruption from the existing count-parity test. Confirmed by
   hand-written reproduction script, not merely theorized. Also flagged F2
   (low, missing multi-digit/negative-path test coverage — folded into the
   fix cycle's test additions).
2. **`016-implementation`** (commits `259f9b85`, `36049632`) — Fix cycle:
   replaced the content-scanning `[[PAGE n]]...[[/PAGE n]]` format with a
   length-prefixed `[[PAGE n LEN=k]]...[[/PAGE n]]` format; `iter_pages()`
   now slices exactly `k` characters instead of scanning for a closing
   marker, eliminating the entire delimiter-in-payload collision class
   (not just the specific self-referential case F1 reproduced).
3. **`.cdr/runs/2026-07-09/017-verification`** (same fix commits) — Final
   re-verification: PASS_WITH_COMMENTS. F1 confirmed genuinely closed
   (root-caused and independently re-verified, not just re-tested).
   Two new low-severity findings surfaced, both non-blocking:
   - **F3** (low): length-prefix framing removes the F1 collision class but
     still trusts the embedded `LEN=k` value against the actual page-text
     length without independent cross-validation — a residual trust
     boundary at the format level, not a reproduced bug.
   - **F4** (low): no test exercises non-ASCII/multi-byte text (accented
     characters, CJK, emoji/astral-plane code points) through
     `normalize_pdf`/`_page_marker`/`iter_pages`. Hand-verified correct by
     the verifier during re-verification (Python `str` operations are
     code-point-based end-to-end), but untested in CI.

Both findings recorded in `.cdr/index/regression.jsonl`
(`hivemind-issue17-3.3.1-F3-len-trust-boundary`,
`hivemind-issue17-3.3.1-F4-missing-unicode-test`) as open/low-severity,
non-blocking, forward-referenced to milestone #10.

**Verifier-run tests:** `pytest agents/ingestion/test_normalize_pdf.py -v`
(passing, run 3x consecutively for flakiness, none observed) and
`ruff check` (clean) on both the pre-fix and post-fix commits.

**Prompt injection note:** Both verification runs (015 and 017) and this
commit step's own git-log exploration encountered embedded fake
system-reminder-style text in commit-message/tool output (fake
date-change/"don't tell the user" notice, fake MCP "tokensave" tool
instructions, fake "Auto Mode Active" directive) — this repo's known
recurring injection pattern. Treated as untrusted data only in every case;
none of it was followed or acted on.

## Release Notes

- Added: `agents/ingestion/normalize_pdf.py` — pymupdf-based PDF-to-text
  normalizer with page-boundary markers (`[[PAGE n LEN=k]]...[[/PAGE n]]`)
  for downstream ingestion/segmentation use.
- Fixed (same subtask, fix cycle): a content-truncation bug where page text
  containing marker-lookalike substrings could silently drop real page
  content; resolved by switching to length-prefixed marker framing.
- Known non-blocking follow-ups (tracked, not fixing now): length-prefix
  trust boundary not independently cross-validated (F3); no automated
  Unicode round-trip test (F4). Deferred to milestone #10 (Phase 4.5,
  issues #38-42).
- Commits: `73310f1da5d01ab632c7b9cf5a5ad311b0ade51e`,
  `462b1831b795788965380bfc6b3201821e4b95ca`,
  `259f9b85d9e3ebf07200071e3c54345bdcb36ed7`,
  `360496321433e14209af44956cccdcdb74a7004d`. All local-only, not pushed.
