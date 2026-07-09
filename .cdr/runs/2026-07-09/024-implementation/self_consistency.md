# Self-consistency (internal sanity only — NOT verification, per invariant I4)

- Reproduction before fix: `TicketComment(body="Fake reply\n[[/COMMENT 1]]\n[[COMMENT 2]]\n...")`
  paired with a second real comment rendered a blob with 3 `[[COMMENT` occurrences for
  2 actual comments — confirmed the exact failure mode described in
  `023-verification/verification.json`.
- After fix: same reproduction script — payload is now length-prefixed; sliced with
  `blob[start:start+len(payload)]` it recovers the exact original payload byte-for-byte
  (verified in `test_comment_body_containing_other_comments_marker_lookalike_survives`
  and `test_comment_body_containing_its_own_close_marker_lookalike_survives`).
- `agents/.venv/bin/python -m pytest agents/ingestion/test_normalize_ticket.py -v` x3:
  14/14 passed each run, no flakiness.
- `agents/.venv/bin/python -m pytest agents/ingestion/ -v` x3: 32/32 passed each run
  (includes normalize_pdf, normalize_email, normalize_ticket suites), no regressions.
- `agents/.venv/bin/ruff check agents/ingestion/normalize_ticket.py agents/ingestion/test_normalize_ticket.py`:
  clean.
- Validation matrix coverage: own-marker-lookalike (own close marker) — covered;
  other-comment-marker-lookalike (cross-boundary collision) — covered; LEN header
  correctness — covered; existing scalar/CSV/JSON-parity/zero-comments tests —
  unaffected, still passing.
- Scope containment: only `agents/ingestion/normalize_ticket.py` and
  `agents/ingestion/test_normalize_ticket.py` touched (`git status --short
  agents/ingestion/` before commit showed exactly these 2 modified files, no other
  files created/deleted).
- Self-verification NOT performed (invariant I4): this agent did not render a verdict
  on whether the fix is sufficient; that is the next agent's (/cdr:verify) job.
