# Validation matrix

| Case | Routing outcome | Covered by | Result |
|---|---|---|---|
| path < Keys[0] ("fruit/apple") | i == 0 (leftmost child) | `TestLookupInternalNodeMultiKeyRouting/leftmost_child` via `Lookup` + `Tree.Lookup` | PASS |
| path == Keys[0] ("fruit/banana") | i == 1 (0 < i < len(Keys), middle child) | `TestLookupInternalNodeMultiKeyRouting/middle_child` via `Lookup` + `Tree.Lookup` | PASS |
| Keys[0] < path < Keys[1] ("fruit/grape") | i == 1 (0 < i < len(Keys), middle child) | `TestLookupInternalNodeMultiKeyRouting/middle_child` via `Lookup` + `Tree.Lookup` | PASS |
| path == Keys[1] ("fruit/kiwi") | i == 2 == len(Keys) (rightmost child) | `TestLookupInternalNodeMultiKeyRouting/rightmost_child` via `Lookup` + `Tree.Lookup` | PASS |
| path > Keys[1] ("fruit/mango") | i == 2 == len(Keys) (rightmost child) | `TestLookupInternalNodeMultiKeyRouting/rightmost_child` via `Lookup` + `Tree.Lookup` | PASS |
| Mutation check: force i to boundary whenever it would be interior | middle_child subtest must fail | manual mutation of `descendToLeaf` (reverted, not committed) | Confirmed FAIL as expected, then reverted |
| Full package regression | all pre-existing tests | `go test ./engine/btree/...` (no `-run` filter) | PASS (ok, 36.9s) |
| Static checks | gofmt / go vet | `gofmt -l`, `go vet ./engine/btree/...` | clean |

Test spec command run verbatim: `go test ./engine/btree/... -run TestLookupInternalNodeMultiKeyRouting` (also run with `-v`) — PASS, exercising the `0 < i < len(Keys)` internal-node routing branch on a real >=2-key node via both `Lookup` and `Tree.Lookup`.
