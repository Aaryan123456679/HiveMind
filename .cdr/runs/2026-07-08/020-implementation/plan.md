# Plan — subtask 3.1.6

1. Create `engine/graph/graph_test.go`.
2. Define a local oracle model:
   - `oracleKey{source, target uint64; typ EdgeType}`
   - `oracle map[oracleKey]CSREdge`, plus an `applyOracle(oracle, source, edge)` helper
     that mirrors `mergeEdges`'s documented semantics exactly (sum Weight + max
     LastUpdated for ENTITY_COOCCUR; last-write-wins by LastUpdated, ties keep the
     newer/later-applied entry, for every other type) — but implemented independently,
     not by calling `mergeEdges`.
   - `oracleNeighbors(oracle, fileID, depth, filter, maxNodes)` — an independent BFS
     over the oracle map mirroring `GraphNeighbors`'s documented dedup/pruning/sort/cap
     semantics (first-seen-hop dedup, filter prunes traversal not just results, sort by
     hop asc/weight desc/target asc, cap after sort).
3. Test body (`TestGraphRoundTrip`):
   a. `t.TempDir()`; open `EdgeLog` at `<tmp>/edgelog`; `graphPath := <tmp>/graph.dat`.
   b. Cycle 1: append a curated, hand-picked set of edges across >=4 distinct source
      fileIDs and all 4 edge types (including repeated ENTITY_COOCCUR pairs with
      different weights/timestamps, and a same-(source,target,type) SPLIT_SIBLING
      repeat to exercise last-write-wins dedup) via `NewCSREdge` + `AppendEdge`,
      updating the oracle identically for each append. Call `Compact`. Assert
      `GraphNeighbors` (several fileID/depth/filter combinations, depth 1 and depth 2)
      against `oracleNeighbors` using `Compact`'s returned in-memory `*CSRGraph`.
   c. Cycle 2: append a second, overlapping batch (new edges to already-existing
      (source,target) pairs from cycle 1, plus brand-new pairs/nodes) to the SAME
      `EdgeLog`. Call `Compact` again against the same `graphPath`/`log`. Re-assert
      `GraphNeighbors` against the updated oracle. This directly targets the
      seam F1/F2 were found at (second compaction round over a previously-compacted
      + previously-truncated node).
   d. Simulated restart: `log.Close()`; discard cycle 2's in-memory `*CSRGraph`; call
      `graph.LoadCSR(graphPath)` fresh to build a brand-new `*CSRGraph`. Re-run the
      exact same `GraphNeighbors` queries from step (c) against the freshly-loaded
      graph and assert IDENTICAL results (both to each other, i.e. pre- vs
      post-restart, and to the oracle) — proving durability survives a restart.
   e. Cycle 3 (post-restart write path): `graph.OpenEdgeLog` again against the SAME
      root (simulating a restarted process reopening its edge log), append a third
      batch (again mixing new pairs and updates to existing pairs, including another
      repeated ENTITY_COOCCUR increment), update the oracle, `Compact` again, then
      `LoadCSR` fresh once more and assert `GraphNeighbors` matches the final oracle
      state. This proves the post-restart CSRGraph/EdgeLog pairing composes correctly
      for further writes, not just reads.
   f. Also assert basic invariants at each checkpoint: `NodeCount`/`EdgeCount` on the
      loaded graph match the oracle's node/edge counts, and `GraphNeighbors` with
      `depth=0` returns nil/no error at least once (boundary check, cheap to include).
4. Comparison helper: convert both `GraphNeighbors` output and `oracleNeighbors`
   output into directly comparable, already-sorted `[]CSREdge` and use
   `reflect.DeepEqual` (or an explicit field-by-field loop for clearer failure
   messages) — do not use `sortedNeighbors` from compact_test.go here since ordering
   is already deterministic and meaningful (hop/weight/target), unlike that helper's
   order-independent Target/Type sort.
5. Run `gofmt -l`, `go vet ./...`, `go build ./...`, then
   `go test ./engine/graph/... -run TestGraphRoundTrip -race -v -timeout 60s`, then
   the full package suite `go test ./engine/graph/... -race -timeout 120s` to confirm
   zero regressions, then the full workspace suite with an explicit timeout to check
   for the pre-known `TestReaderDuringSplit` flake (already tracked, not this
   subtask's concern) and nothing else.
6. Self-consistency check (internal sanity only, not verification): confirm build/vet/
   gofmt clean and every acceptance-criterion row in validation-matrix.json is covered
   by an assertion in the new test.
7. One local git commit (`test:` prefix, Problem/Solution/Impact body per user's
   commit standard), no push.
8. handoff.json with pointers only.
