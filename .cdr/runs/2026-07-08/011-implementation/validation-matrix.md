| # | Scenario | Test | Result |
|---|----------|------|--------|
| 1 | F2 exact repro: append, compact (full success), append again same node, compact again -> both edges reflected | `TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost` (3 rounds) | PASS (fails against pre-fix 9850083, confirmed via worktree revert-experiment) |
| 2 | F1 regression unmodified | `TestCompaction_RetryAfterTruncateFailureDoesNotDoubleCountWeight` | PASS unmodified |
| 3 | Seam: failed-truncate retry (F1) then ordinary subsequent appends+compactions (F2) same node | `TestCompaction_FailedTruncateRetryThenOrdinarySubsequentAppendsSurvive` | PASS (fails against pre-fix 9850083) |
| 4 | Existing TestCompaction subtests, TestTruncateNode, other crash-injection tests | full graph package suite | PASS, no regressions |
| 5 | gofmt / go vet / go build ./... | n/a | clean |
| 6 | `-race` graph + wal packages | `go test ./graph/... ./wal/... -race -count=1 -timeout 10m` | PASS |
| 7 | Full module suite | `go test ./... -count=1 -timeout 25m` | PASS (split package's `TestReaderDuringSplit` is a pre-existing, unrelated timing flake - confirmed unaffected by this diff by reproducing on both pre-fix HEAD and post-fix tree across repeated runs) |
