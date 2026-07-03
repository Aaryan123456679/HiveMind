# Plan

1. `engine/catalog/record.go`:
   - Change `Encode()` signature to `func (r CatalogRecord) Encode() ([]byte, error)`.
   - Compute `count := len(r.RedirectTargetIDs)` up front; if `count > MaxRedirectTargets`,
     return `(nil, fmt.Errorf(...))` before allocating/writing the buffer — no partial
     truncated buffer is ever returned.
   - Remove the old `if count > MaxRedirectTargets { count = MaxRedirectTargets }`
     clamp — it's now unreachable/removed since the guard above returns early.
   - Update doc comment: remove "Encode truncates defensively rather than panicking",
     replace with description of the hard-error behavior, matching Decode()'s symmetry.

2. `engine/catalog/record_test.go`:
   - `TestRecordEncodeDecode`: capture `encoded, err := tt.rec.Encode()`, fail fast on
     unexpected error.
   - Add a reserved-padding-bytes-stay-zero assertion inside the same subtest loop
     (optional nice-to-have from the verifier, small addition).
   - `TestDecodeRejectsOutOfRangeRedirectCount`: update to `buf, err := CatalogRecord{}.Encode()`
     and check the err before mutating `buf[offRedirectCount]`.
   - Add `TestEncodeRejectsTooManyRedirectTargets`: 10 redirect targets (> 8), assert
     non-nil error and nil buffer.
   - Add `TestEncodeAcceptsExactlyMaxRedirectTargets`: exactly 8 redirect targets,
     assert no error and correct buffer size (boundary case).

3. Verify no other call sites of `.Encode()` exist in the engine tree besides
   `record_test.go` (confirmed via grep — none).

4. Run:
   - `go test ./engine/catalog/... -run TestRecordEncodeDecode -race -v`
   - `go vet ./engine/catalog/...`
   - `go build ./engine/...`

5. Commit as one local commit (Problem/Solution/Impact), no push.

6. Update `.cdr/index/task.jsonl` entry `task-1.1.1`: state back to `implemented`,
   commit set to the new SHA, verification left `null` pending re-verification.
   Leave `.cdr/runs/2026-07-03/004-verification/` untouched (verifier's historical
   artifact).
