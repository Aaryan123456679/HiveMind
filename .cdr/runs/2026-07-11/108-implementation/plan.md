# Plan

1. Hand-construct a fresh, isolated on-disk fixture (own `t.TempDir()`, own
   `NodeStore`) with a root internal node holding exactly 2 keys and 3
   children — the minimal shape that makes `0 < i < len(Keys)` reachable at
   all (`i` can be 1, satisfying `0 < 1 < 2`).
2. Keys: `["fruit/banana", "fruit/kiwi"]`; Children: 3 leaves —
   `leafA` (covers < "fruit/banana"), `leafB` (covers
   ["fruit/banana","fruit/kiwi") — the middle/target child), `leafC` (covers
   >= "fruit/kiwi").
3. Assert `sort.Search` semantics land on `i==1` (middle child) for a path
   equal to `Keys[0]` and a path strictly between `Keys[0]` and `Keys[1]`.
4. Exercise routing via BOTH: (a) the free-function `Lookup` (exercises
   `descendToLeaf`), and (b) `Tree.Lookup` (exercises `lookupOnce`), since
   the acceptance criteria names `Tree.Lookup` specifically and it is a
   separate implementation of the same shape.
5. Add companion subtests for the `i==0` (leftmost) and `i==len(Keys)`
   (rightmost) cases on this SAME multi-key node, so the middle-child
   assertion can't be passing "by accident" if some other routing bug
   happened to coincidentally also land on the middle leaf.
6. Self-consistency: temporarily mutate `descendToLeaf`'s routing to always
   force `i` to a boundary value when it would otherwise be strictly
   interior, confirm the new test fails (proves it is a load-bearing,
   non-vacuous test), then restore the original `lookup.go` unchanged and
   confirm `go build`/full package tests pass again.
7. `gofmt`/`go vet` clean, full `go test ./engine/btree/...` green.
8. One local commit (Problem/Solution/Impact), no push.
9. Handoff to `/cdr:verify`.
