# Plan — Subtask 2b.3.1

1. Add `engine/split/execute.go`:
   - `ExecuteSplitAllocateAndWrite(idAlloc *catalog.IDAllocator, cs *catalog.ContentStore, originalContent []byte, plan SplitPlan) (map[string]uint64, error)`.
   - Validation pass (before any allocation/write, so a rejected plan leaves no
     partial state):
     - `plan.Files` must be non-empty.
     - Every `SplitFileProposal.NewPath` must be non-empty and unique within the
       plan.
     - Every `SplitFileProposal` must have at least one `SectionRange`.
     - Every `SectionRange` must satisfy `0 <= Start <= End <= len(originalContent)`.
     - No two `SectionRange`s -- across ANY proposals in the plan, including within
       the same proposal -- may overlap (byte-level disjointness check via a
       sorted-interval scan). Zero-length ranges (`Start == End`) are allowed and
       trivially never overlap anything.
     - On any validation failure: return a wrapped, descriptive error and perform
       NO allocation and NO writes.
   - Execution pass (only after full validation succeeds):
     - For each proposal, allocate one new fileID via `idAlloc.Next()`.
     - Concatenate `originalContent[r.Start:r.End]` for each `SectionRange` in
       order to build the new file's full content.
     - Durably write that content to `cs.ContentPath(newFileID)` using a local
       temp-file+rename helper mirroring `catalog.ContentStore`'s existing
       `writeContentFile` pattern (CreateTemp in the same dir -> Write -> Sync ->
       Rename).
     - Record `NewPath -> newFileID` in the result map.
   - Return the accumulated map on success.
2. Add `engine/split/execute_test.go` with `TestSplitAllocateAndWrite` (+ a few
   focused subtests for validation-error edge cases: out-of-bounds range, empty
   plan, duplicate NewPath, overlapping ranges) using `FixtureSplitPlan` /
   `FixtureFileContent` from `proposer_mock.go` as the primary fixture, backed by a
   real `catalog.ContentStore` opened in `t.TempDir()`.
3. Run `go build ./... && go vet ./... && gofmt -l .` from `engine/`.
4. Run `go test ./split/... -run TestSplitAllocateAndWrite -count=1 -timeout 5m`.
5. Run full `go test ./split/... -race -v -count=1 -timeout 10m` (regression check
   for 2b.1.1-2b.1.3, 2b.2.1-2b.2.2).
6. Run `go test ./catalog/... -count=1 -timeout 5m` (regression check since
   `ContentStore.ContentPath`/`catalog.IDAllocator` are reused, read-only).
7. One local commit (Problem/Solution/Impact style), no push.
8. Write self-consistency.json + handoff.json.
