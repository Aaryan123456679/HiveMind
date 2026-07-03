| # | Case | Expected | Result |
|---|------|----------|--------|
| 1 | `Encode()` zero-value record | success, round-trips via `Decode` | PASS |
| 2 | `Encode()` populated record, 3 redirect targets | success, round-trips | PASS |
| 3 | `Encode()` exactly `MaxRedirectTargets` (8) targets | success (boundary, not an error) | PASS |
| 4 | `Encode()` active status, zero redirect targets | success, round-trips | PASS |
| 5 | Reserved padding bytes (`offReserved..offReserved+reservedWidth`) | remain zero after round-trip | PASS (new assertion) |
| 6 | `Encode()` with 10 redirect targets (> `MaxRedirectTargets`=8) | non-nil error, nil buffer, no truncation/data loss | PASS (new test: `TestEncodeRejectsTooManyRedirectTargets`) |
| 7 | `Decode()` wrong buffer length (short/long) | error | PASS (pre-existing, unaffected) |
| 8 | `Decode()` out-of-range redirect count in buffer | error | PASS (updated call site for new signature) |

Commands run:
- `go test ./engine/catalog/... -run TestRecordEncodeDecode -race -v` -> PASS (4/4 subtests)
- `go test ./engine/catalog/... -race -v` (full package) -> PASS (5/5 tests)
- `go vet ./engine/catalog/...` -> clean
- `go build ./engine/...` -> clean
