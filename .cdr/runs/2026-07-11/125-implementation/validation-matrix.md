# Validation matrix

| Requirement | Covered by | Result |
|---|---|---|
| Dedup repeated terms before scan loop | `dedupTerms` (search_candidates.go) + `TestDedupTermsCollapsesRepeatedTerms` | PASS |
| Repeated-term query does not scan N times / does not hit maxQueryTerms via repeats | `TestSearchCandidatesRepeatedTermScansOnce` (query repeats one term `maxQueryTerms*3` times, must succeed) | PASS |
| Reject pathological distinct-term count | `maxQueryTerms`, `validateQueryTermCount`, `SearchCandidates` (server.go) + `TestSearchCandidatesRejectsTooManyDistinctQueryTerms` (maxQueryTerms+1 -> InvalidArgument; exactly maxQueryTerms -> success) | PASS |
| Existing ranking/merge behavior unaffected | Full `go test ./rpc/...` (pre-existing TestSearchCandidates*, TestSearchCandidatesMultiWordQuery) | PASS |
| perTermPoolCap/mergedPoolCap docs corrected (retained-memory only, not scan cost) | doc comments in search_candidates.go + docs/LLD/query-agent.md + docs/LLD/btree.md | Updated |
| Build/vet clean | `go build ./rpc/... ./btree/...`, `go vet ./rpc/... ./btree/...` in isolated disposable worktree at commit-tree `0e9babf` | PASS (self-consistency only, not verification) |

Isolated verification method: private `GIT_INDEX_FILE` (git read-tree HEAD@73aa6f8 +
git update-index --cacheinfo for the 5 blobs + git write-tree) -> disposable
`git commit-tree <tree> -p HEAD` -> `git worktree add --detach` -> build/test/vet -> worktree
removed. This avoided the shared repo's actively-racy working tree/index (other concurrent
agents mutating both) while still validating exactly the tree that was committed.
