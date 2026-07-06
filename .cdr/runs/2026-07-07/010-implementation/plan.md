# Plan — subtask 2b.2.1

## Goal
Add `engine/split/proposer.go` defining a local `SplitProposer` interface plus the minimal
`SplitPlan` supporting types, with zero gRPC/network dependency, plus a trivial fake in
`engine/split/proposer_test.go` (`TestSplitProposerInterface`) proving the interface
contract is satisfiable.

## Steps

1. Create `engine/split/proposer.go`:
   - Package doc comment explaining scope boundary (mirrors `orchestrate.go`'s style):
     this is a proposal-only interface; issue #12 owns real allocation/execution; the real
     gRPC-backed implementation lands in a later epic once `proto/`/`agents/ingestion`
     exist in Go-consumable form.
   - `SectionRange{Start, End int}` — half-open `[Start,End)` byte-offset range into the
     original `fileContent`.
   - `SplitFileProposal{NewPath string; SectionRanges []SectionRange}`.
   - `SplitPlan{Files []SplitFileProposal; RedirectSummary string}`.
   - `SplitProposer` interface: `ProposeSplit(fileContent []byte) (SplitPlan, error)`.
   - No imports beyond nothing needed (interface/struct defs only) — confirm no import
     line is even necessary; if a doc-comment cross-reference needs `catalog`/`wal` it must
     NOT be added (this file must depend on neither, keeping it decoupled per the
     acceptance criteria — the "no direct gRPC/agent dependency" bar plus the general
     spirit of decoupling `SplitProposer` from the rest of the package's storage
     internals).
2. Create `engine/split/proposer_test.go`:
   - `package split`
   - A trivial unexported fake type, e.g. `fakeSplitProposer struct{ plan SplitPlan; err error }`
     with a `ProposeSplit` method satisfying the interface, returning the canned
     `plan`/`err`.
   - `TestSplitProposerInterface`: constructs a `fakeSplitProposer`, assigns it to a
     `SplitProposer`-typed variable (compile-time interface-satisfaction check), calls
     `ProposeSplit` with sample `[]byte` content, asserts the returned `SplitPlan` matches
     the fixture (`Files`/`RedirectSummary` fields) and `err` is nil; a second case asserts
     a non-nil error is propagated unchanged.
3. Do not modify `trigger.go`, `guard.go`, `orchestrate.go`, or their tests.
4. Do not wire `SplitProposer` into `Orchestrator` — that is out of scope (later
   subtask/epic once a real implementation exists).

## Verification commands (self-consistency only, step 5)
Run from `engine/`:
- `go build ./... && go vet ./... && gofmt -l .` (expect gofmt to print nothing)
- `go list -deps ./split/... | grep -i grpc` (expect empty output)
- `go test ./split/... -run TestSplitProposerInterface -count=1 -timeout 5m`
- `go test ./split/... -race -v -count=1 -timeout 10m` (full package, zero regressions to
  2b.1.1/2b.1.2/2b.1.3 tests)
