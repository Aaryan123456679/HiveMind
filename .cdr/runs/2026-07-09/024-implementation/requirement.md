# Requirement (fix cycle, attempt 1 of 3)

Fix CHANGES_REQUESTED finding from `.cdr/runs/2026-07-09/023-verification/verification.json`
(subtask issue-17-3.3.3-support-ticket-normalizer, reviewed commit `770788bb54eb738e7f418e4eefcfb6a28b112993`):

`_comment_block` in `agents/ingestion/normalize_ticket.py` (lines ~217-224) used the
ORIGINAL vulnerable `[[COMMENT n]]`/`[[/COMMENT n]]` content-scanning marker design
that `agents/ingestion/normalize_pdf.py` (subtask 3.3.1) already had and already fixed
via length-prefixed framing. A comment body containing a literal substring matching a
close marker (its own or another comment's) silently desynchronizes section boundaries.

Required changes (per verification's `required_changes`):
1. Replace the marker scheme with length-prefixed framing mirroring normalize_pdf.py:
   `[[COMMENT n LEN=k]]\n<payload>[[/COMMENT n]]`.
2. Add regression tests for a comment body containing a lookalike of its own close
   marker, and of another comment's marker.
3. Update the module docstring's "reliably parse ... back out" claim to be accurate.
