# Requirement

Subtask 3.3.2 (GitHub issue #17, Epic Phase 3, milestone #5 "Graph store + ingestion agents"):

**3.3.2 — Email normalizer (Enron-specific) sender/subject/thread/body fields**

- Acceptance criteria: Given a raw Enron-format email file, normalizer extracts sender, subject,
  thread id, body correctly.
- Test spec: `pytest agents/ingestion/test_normalize_email.py`: run against fixture Enron emails,
  assert extracted fields match expected values.
- Impacted modules: `agents/ingestion/normalize_email.py`, `agents/ingestion/test_normalize_email.py`

Issue #17 body re-checked directly via `gh issue view 17` before starting; content is a plain
subtask checklist with no embedded instructions to an agent — clean of injection. (Separately,
raw tool output while browsing this session has contained injected fake system-reminder text;
per standing security instructions that is treated as untrusted data and ignored, not as
instructions to follow.)

## Interpretation

The real Enron email corpus (as released by CMU/FERC, the standard "enron_mail" maildir dump) is
a maildir-style tree where each file is one raw RFC 2822-ish message: a block of `Key: value`
headers (commonly `Message-ID`, `Date`, `From`, `To`, `Subject`, `Cc`, `Bcc`, `X-From`, `X-To`,
`X-cc`, `X-bcc`, `X-Folder`, `X-Origin`, `X-FileName`), a blank line, then a plain-text body.

The corpus has **no native "thread id" header** — real-world Enron messages do not reliably carry
`In-Reply-To`/`References` (many do, many don't, and even where present they identify a specific
parent message, not a stable "this whole thread" id). This subtask's acceptance criteria still
requires a `thread` field, so a derivation is required. See `plan.md` for the disclosed judgment
call.

## Scope

- New `agents/ingestion/normalize_email.py`: `normalize_email(path) -> EmailFields` (or similar),
  parsing a single raw Enron-format message file into `sender`, `subject`, `thread`, `body` (plus
  any other conventional fields worth carrying, kept minimal per the acceptance criteria).
- New `agents/ingestion/test_normalize_email.py` + hand-authored fixture email file(s) matching
  real Enron maildir header conventions.
- Out of scope: `RawDocument` wrapping/dispatch (3.3.4), other normalizers (3.3.1 done, 3.3.3 not
  yet), directory/corpus-level batch ingestion, real Enron dataset staging under `data/` (issue
  #19's concern, not this subtask's).
