# Self-consistency (internal sanity only — not verification)

- Build green: `go build ./engine/...` clean.
- Targeted test green: `go test ./engine/catalog/... -run TestRecordEncodeDecode -race -v`
  passes (4/4 subtests), matching the exact command specified in the task brief.
- Full package test green: `go test ./engine/catalog/... -race -v` passes (5/5 tests,
  including the 2 new ones).
- `go vet ./engine/catalog/...` clean.
- Validation matrix (validation-matrix.md) covers every required case: boundary success
  at exactly MaxRedirectTargets, hard error above MaxRedirectTargets, existing
  round-trip cases still pass under the new `([]byte, error)` signature, and the
  optional reserved-padding-zero assertion.
- Grepped `engine/` for other `.Encode()` call sites outside `record_test.go` — none
  found, so no other production code needed updating for the signature change.
- No independent verification performed here (invariant I4) — a separate
  re-verification pass will assess this fix against the CHANGES_REQUESTED finding.
