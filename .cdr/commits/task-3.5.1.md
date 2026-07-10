# task-3.5.1: Bitext + Enron dataset loaders (issue #19, first of 2 subtasks)

## Summary

Issue #19 (dataset ingestion epic) subtask 3.5.1 required loader scripts
that fetch/read the Bitext customer-support-ticket dataset and an Enron
email subsample, yielding `RawDocument`-ready inputs for
`agents/ingestion`'s existing normalizers. `data/` previously had no
Python code, only a README describing this scope. This subtask adds two
loaders: `data/load_bitext.py`, backed by a genuine 30-row sample of the
real public Hugging Face Bitext dataset (downloaded once, committed with
disclosed provenance/license metadata), and `data/load_enron.py`, backed
by 3 hand-authored, format-faithful Enron-style message files (disclosed
in the module docstring as invented content, not sourced from the real
423MB Enron corpus). Both fixture choices are squarely within scope: issue
#19's own test spec explicitly calls for testing "against a small local
fixture subset of each dataset," and real/non-fixture corpus usage is
explicitly deferred by the issue to subtask 3.5.2. The Bitext fixture
additionally satisfies the issue's "downloaded sources" language literally
(a real HF download); the Enron fixture does not (it is not a subsample of
anything), which is why it is flagged below rather than treated as a full
match.

## Features

- **`data/load_bitext.py`**: low-level `iter_bitext_records()` reading
  `data/fixtures/bitext_sample.json` (a genuine 30-row sample fetched once
  from Hugging Face's `datasets-server` API, committed with `_source`,
  `_license` (`CDLA-Sharing-1.0`), and `_fetched_via` provenance metadata
  at the top level) with zero dependency on `agents/`; plus
  `bitext_row_to_ticket_json`, mapping each row onto the exact field set
  `normalize_ticket_json` reads (`ticket_id`, `subject`, `description`,
  `status`, `priority`, `category`, `requester`, `assignee`, `created_at`,
  `comments`).
- **`data/load_enron.py`**: low-level path/record iterator over a local
  maildir-style directory, defaulting to
  `data/fixtures/enron_sample/` (3 files: a plain message, a reply, and
  one deliberately omitting optional headers to exercise
  `normalize_email`'s must-not-raise-on-absence contract); plus
  `load_enron_documents`, which is written generally enough to read a real
  `maildir/`-style directory unchanged once one exists.
- **Lazy import of `agents/ingestion` from both loaders**: neither module
  imports `ingestion.dispatch` at module level -- the import happens only
  inside the `RawDocument`-building entry points
  (`load_enron_documents`, and the ticket-JSON path), so the low-level
  record iterators are importable and usable standalone, verified working
  under a bare `/usr/bin/python3` with no `agents/.venv` or repo `PATH`
  wiring present, i.e. genuinely CWD/venv-independent.
- **`data/test_loaders.py`**: 14 tests covering both loaders against their
  local fixtures per the issue's test spec (record counts, field
  presence, the missing-optional-headers case), no network required.

## Impact

- `data/` now has real, testable dataset-ingestion inputs. No changes to
  `agents/ingestion`'s normalizers, `dispatch.py`, `segment.py`,
  `wiring.py`, `propose_split.py`, `shortlist.py`, or any `engine/` Go
  code -- purely additive under `data/`.
- Full regression suite (`agents/.venv/bin/pytest data/ agents/ -q`): 169
  passed, no new failures. `ruff check data/`: clean.
- This is the first of issue #19's 2 subtasks. Subtask 3.5.2 (full
  end-to-end ingestion smoke run) is not yet done and cannot be until a
  real Enron sample is sourced -- see Release Notes below.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-10/038-verification`
- Verifier independently ran `agents/.venv/bin/pytest data/ agents/ -q`
  (169 passed), `agents/.venv/bin/ruff check data/` (clean), and confirmed
  the lazy-import claim under a bare non-venv `python3`. Field mapping
  correctness, provenance/licensing, and Enron-disclosure honesty were all
  independently re-derived rather than taken on the implementer's word.
  Zero blocking findings. Two non-blocking comments: (1) the Enron fixture
  is hand-authored, not a real corpus subsample -- acceptable for 3.5.1 per
  the issue's own fixture-based test spec, but 3.5.2 will need a genuine
  Enron sample; (2) a cosmetic docstring inaccuracy (`agents.ingestion.dispatch`
  referenced in prose where the code correctly imports `ingestion.dispatch`).

## Release Notes

- Added `data/load_bitext.py` and `data/load_enron.py`: dataset loaders
  mapping the Bitext support-ticket dataset and an Enron-format email
  sample onto `agents/ingestion`'s existing `normalize_ticket`/
  `normalize_email` normalizer inputs, both importable independent of
  the repo's CWD or `agents/.venv`.
- Added `data/fixtures/bitext_sample.json` (real 30-row HF download, with
  disclosed provenance/license) and `data/fixtures/enron_sample/`
  (3 hand-authored, format-faithful fixture files, explicitly disclosed as
  invented rather than real Enron correspondence).
- **New finding, flagged forward to 3.5.2**
  (`hivemind-issue19-3.5.2-need-real-enron-sample`): subtask 3.5.2 will
  need a genuine, non-fixture Enron sample. `load_enron_documents` already
  works unchanged against a real `maildir/`-style directory once one
  exists -- no loader code changes are anticipated -- but the current
  hand-authored fixtures are sufficient only for 3.5.1's unit-level loader
  testing, not for the spirit of a real end-to-end smoke run. Recorded in
  `.cdr/index/regression.jsonl` and `.cdr/memory/pending.md`.
- Issue #19 has 1 subtask remaining: 3.5.2 (full end-to-end ingestion
  smoke run), which will also need to address the still-open, carried-forward
  F4 finding (`engine/rpc/server.go`'s `PutSegment` CREATE path never
  setting `catalog.CatalogRecord.PathHash`, high severity but non-blocking,
  first surfaced during issue #18 subtask 3.4.4's verification) since a
  real end-to-end run is the first context where that gap could actually
  bite. This commit does not push and does not touch any GitHub
  issue/milestone state -- that requires separate, fresh user
  authorization.
