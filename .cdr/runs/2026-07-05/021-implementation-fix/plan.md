# Plan

1. Reproduce the reported bug with a scratch adversarial test (64
   goroutines, ~30,000 keys) before touching any code -- confirm the exact
   error string from the verifier's finding.
2. Root-cause via diagnostic instrumentation (temporary, reverted before
   finishing): trace the exact node states at the moment of failure.
3. Fix `Tree.findParent`'s `isLeaf` branch to walk the `NextLeaf` chain
   (bounded, since a leaf chain is finite) looking for `childID`, returning
   the tracked `ancestorID` on success; add a defensive guard for the
   (should-be-impossible) bare-leaf-root case.
4. Re-run the original repro (same 64-goroutine/~30,000-key scenario) many
   times (40 attempts) to confirm the fix eliminates the failure, not just
   makes it rarer.
5. Strengthen `assertStructuralInvariants` to validate, per internal level:
   exactly one `NextSibling`-chain head (`LowKey == ""`), the chain visiting
   every node at that level exactly once in strictly increasing
   subtree-min-key order, and every non-head node's `LowKey` exactly
   matching its own subtree's minimum key.
6. Add `testCrabbingInsertDeepOverlappingSubtree` (new `TestCrabbingInsert`
   subtest) at the proven-sufficient scale (64 goroutines, ~30,080 keys) to
   permanently exercise the previously-blind depth->=2 concurrent-split
   regime, with an explicit depth->=2 sanity assertion.
7. Delete the scratch repro test file (not part of the shipped suite).
8. Full validation: `go build ./...`, `go vet ./...`, `gofmt -l`, full
   `go test ./engine/btree/... -race -v -count=1` (zero regressions), and
   the new/strengthened concurrent subtest run repeatedly.
9. Write CDR fix artifacts, one commit (no push), update
   `.cdr/index/task.jsonl` and append resolution info to
   `.cdr/index/regression.jsonl`'s existing 020-verification/2a.4.2 entry.
