# Architecture Discovery

## Index consulted (`.cdr/index/`) — read before source, per token order
- `file.jsonl`: `agents/ingestion/normalize_pdf.py` and `agents/ingestion/test_normalize_pdf.py`
  now present (from 3.3.1, runs 014/016/017). No entries yet for `normalize_email.py` — greenfield
  for this file.
- `task.jsonl` / `regression.jsonl`: only 3.3.1's F1 (marker-collision) entry, resolved. Nothing
  relevant to email parsing.
- `decision.jsonl` / `feature.jsonl`: no email-normalizer-related prior decisions.

## HLD / LLD
- `docs/HLD.md`: system context, not re-read in full (out of scope of this targeted change).
- `docs/LLD/ingestion-agent.md`: "Email: via stdlib / Enron-specific parsing -> sender/subject/
  thread/body fields." Confirms: (a) stdlib `email` module is the intended parsing tool (matches
  issue's implicit hint and general Python practice), (b) target fields are exactly
  sender/subject/thread/body, matching the issue text verbatim, (c) output ultimately feeds the
  common `RawDocument{id, sourceType, text, structuredFields, timestamp}` record — but that
  wrapping is subtask 3.3.4, out of scope here (same boundary as 3.3.1).

## Prior subtask (3.3.1) conventions carried forward
Read `agents/ingestion/normalize_pdf.py` + `agents/ingestion/test_normalize_pdf.py` directly (repo
LLD/index gave no email-specific precedent, so the sibling normalizer is the load-bearing
precedent for package conventions):
- Module docstring explains format/contract up front; public functions have Google-style
  docstrings (Args/Returns/Raises).
- `from __future__ import annotations`; type hints on all public functions
  (`str | Path` for path args).
- Public regex/format constants exposed for downstream consumers (e.g. `PAGE_MARKER_RE`) — so
  `RawDocument`/dispatch (3.3.4) can reuse parsing primitives without re-deriving them.
- Errors are not swallowed: let the underlying parser's exceptions propagate (pymupdf's exceptions
  for 3.3.1); no bespoke error-handling layer added beyond what's needed for the acceptance
  criteria.
- Test style: plain `pytest` functions + fixtures, `tmp_path`-based where a file needs to exist on
  disk. 3.3.1 fixture was built *programmatically* (via `fitz` itself) because PDF is a binary
  format with no reasonable hand-authored text form.
- Package structure: `agents/ingestion/__init__.py` is empty (no re-exports there); consumers
  import `from ingestion.normalize_pdf import normalize_pdf, ...` directly.
- `agents/pyproject.toml`: no new dependency needed for 3.3.1 (pymupdf already declared); stdlib
  `email` module for 3.3.2 needs **no new dependency** either — confirmed no changes to
  `pyproject.toml` required.
- `agents/.venv/bin/pytest` exists and is the venv to run tests through (matches 3.3.1's workflow).

## Divergence from 3.3.1 (disclosed, not silent)
Unlike PDF, Enron-format email is a plain-text-ish format (headers + blank line + body) — a real
parser (`email.parser`) run against a *hand-authored* raw-text fixture is both feasible and the
correct test design (per task instructions), rather than programmatic generation. Confirmed via
`find`/`grep` across the repo (including `data/`, which only has a `README.md` describing planned
future dataset staging for issue #19 — no actual Enron files exist yet) that no real Enron sample
data is currently staged anywhere in this repo, so fixtures must be hand-authored here.

## Thread id: no native header in the corpus
Real Enron corpus messages do not have a standard header. `In-Reply-To`/`References` do appear on
*some* Enron messages (the corpus is real Outlook/Notes export, standard headers can survive), but
are absent on plenty of others (as this is a subtask-3.3.2 concern, this normalizer must produce
*some* thread id even for the (typical) case with no `In-Reply-To`/`References`), so a fallback
derivation is required and disclosed in `plan.md`.
