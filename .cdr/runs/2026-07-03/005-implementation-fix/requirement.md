# Requirement (fix for verification finding on subtask 1.1.1)

This is NOT a new subtask. It is a follow-up fix for subtask 1.1.1 (GitHub issue #1),
same acceptance criteria as before: "encodes/decodes from fixed-size byte layout with
no data loss for all fields."

Independent verification (run `004-verification`, verdict `CHANGES_REQUESTED`) found:

- **High severity (requirements_conformance)**: `CatalogRecord.Encode()` silently
  truncated `RedirectTargetIDs` to `MaxRedirectTargets` (8) when the caller passed more,
  returning no error. This directly contradicts the "no data loss for all fields"
  acceptance criterion. `Decode()` already symmetrically rejects an out-of-range
  redirect count with an error; `Encode()` did not mirror that behavior.
- **Medium severity (test_coverage)**: the truncation/overflow path in `Encode()` was
  completely untested.
- **Low severity (maintainability, optional/non-blocking)**: reserved padding bytes
  (`offReserved`/`reservedWidth`) were documented as staying zero but no test asserted
  this across a round trip.

Required fix (from task brief):
1. Change `Encode()` signature to `func (r CatalogRecord) Encode() ([]byte, error)`,
   returning an error when `len(r.RedirectTargetIDs) > MaxRedirectTargets` instead of
   truncating. Update doc comment (remove "truncates defensively" language).
2. Update all call sites in `record_test.go` for the new `([]byte, error)` signature.
3. Add a new test exercising the overflow path (10 > MaxRedirectTargets=8) asserting a
   non-nil error, plus confirm the existing boundary case (exactly 8) still succeeds
   under the new signature.
4. Optional: assert reserved padding bytes stay zero after round-trip.

Verification artifacts referenced (read-only, not modified):
- `.cdr/runs/2026-07-03/004-verification/verification.json`
- `.cdr/index/regression.jsonl` (last entry, subtask 1.1.1)
