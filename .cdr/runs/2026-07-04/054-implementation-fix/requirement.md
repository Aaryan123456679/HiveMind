# Fix requirement (responding to 053-verification CHANGES_REQUESTED)

Source: `.cdr/runs/2026-07-04/053-verification/verification.json`

Blocking finding (edge_cases: fail; architecture_conformance: concern; test_coverage: concern):
`ContentStore.Append` performs a fully unsynchronized read-modify-write of the content
file (read existing bytes -> append -> write back), with no per-fileID lock, unlike
`Catalog`'s own striped-mutex design (`engine/catalog/catalog.go` `stripes [numStripes]sync.Mutex`,
keyed by `stripeFor(fileID)`). Reproduced: 50 concurrent 1-byte `Append()` calls against the
same fileID silently lost 49 of 50 appends (final content length 1, not 50); catalog
`SizeBytes` matched the corrupted result with no error surfaced.

Everything else in the verification (boundary/threshold-crossing semantics, WAL-before-apply
ordering, cumulative sizeBytes accounting, edge cases other than concurrency, security,
performance, maintainability) passed and must NOT be reworked.

## Scope
1. Serialize `Append`'s read-modify-write critical section per fileID, reusing the repo's
   existing striped-mutex convention (not a new locking primitive).
2. Confirm `Create` and `Read` do or don't need the same protection (document the reasoning).
3. Add a `-race` regression test reproducing the exact repro (N concurrent same-fileID Appends,
   assert final length/SizeBytes reflects all appends).
4. Do not touch anything else in `engine/catalog/content.go` beyond what's needed for the fix.
