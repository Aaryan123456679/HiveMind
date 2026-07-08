# Plan — Subtask 3.1.5

1. Create `engine/graph/traverse.go`:
   - Package doc comment (file-level) explaining: this is subtask 3.1.5's read-only
     traversal API; compacted-only design decision + rationale (pointer to
     docs/LLD/graph.md, cross-reference requirement.md's evidence); BFS/cap/ordering
     contract.
   - `func GraphNeighbors(g *CSRGraph, fileID uint64, depth int, edgeTypeFilter EdgeType, maxNodes int) ([]CSREdge, error)`
     - Validate `depth` in `[0, 2]`; error otherwise (`"graph: depth %d out of range [0, 2]"`).
     - Validate `maxNodes >= 0`; error otherwise.
     - Validate `edgeTypeFilter` is either `EdgeTypeInvalid` (sentinel: no filter) or one
       of the 4 `ValidEdgeType` values; error otherwise (reject silently-matching-nothing
       typos).
     - If `g == nil` or `depth == 0` or `maxNodes == 0`: return `nil, nil` (no error;
       degenerate-but-valid inputs).
     - BFS from `fileID`, hop by hop up to `depth`:
       - Maintain `visited map[uint64]int` (fileID -> hop distance first seen at), seeded
         with `fileID -> 0` (source itself, never included in results).
       - Maintain `result []CSREdge` plus, per result entry, which hop it was found at (an
         internal `candidate{edge CSREdge, hop int}` slice, hop info dropped before
         returning).
       - At each hop, expand the frontier (nodes newly discovered at hop-1) via
         `g.Neighbors(node)`, skip edges whose `Type` does not match `edgeTypeFilter`
         (unless sentinel = match-all), skip targets already in `visited` (do not
         re-add/re-rank a node reached via a shorter or equal-length earlier path), record
         first-seen hop for newly discovered targets.
     - Sort candidates by (hop ascending, Weight descending, Target ascending).
     - Truncate to `maxNodes`.
     - Return the `[]CSREdge` slice (hop metadata dropped, not part of the public
       `CSREdge` shape).
2. Create `engine/graph/traverse_test.go`:
   - `TestGraphNeighbors` top-level, subtests:
     - `Basic1Hop` — simple star graph, depth=1, no filter, no cap: all direct neighbors
       returned.
     - `TwoHopExpansion` — chain graph a->b->c->d, depth=2 from a: b (hop1), c (hop2)
       returned; d excluded (3 hops away). Also assert d is NOT included when depth=2 but
       reachable via a 3rd hop.
     - `CapExactlyN` — >maxNodes reachable nodes; maxNodes == exact reachable count: no
       truncation.
     - `CapNPlus1` — maxNodes == reachable count - 1: exactly one dropped, the
       lowest-ranked (lowest Weight, or highest Target on tie) survivor differs
       predictably.
     - `CapZero` — maxNodes == 0: empty, non-nil-vs-nil-agnostic (assert `len(result) ==
       0`), no error.
     - `DepthZero` — depth == 0: empty result (fileID itself never included), no error.
     - `TypeFilterNoMatches` — edgeTypeFilter set to a type present nowhere in the graph:
       empty result, no error.
     - `TypeFilterAllTypesRequested` — build a mixed-type graph, call once per each of the
       4 valid types plus once with `EdgeTypeInvalid` (no filter): assert each call returns
       exactly the edges of that type (or all, for no-filter).
     - `TypeFilterSingleType` — mixed-type graph, filter to exactly one type: only that
       type's edges appear, at any hop.
     - `InvalidDepthRejected` — depth = -1 and depth = 3: both return an error.
     - `InvalidEdgeTypeFilterRejected` — an undefined byte value (e.g. 200) as
       `edgeTypeFilter`: error, not silently-empty.
     - `NegativeMaxNodesRejected` — maxNodes = -1: error.
     - `DedupNoDoubleCounting` — a diamond graph (a->b, a->c, b->d, c->d): d reached via two
       different 2-hop paths must appear exactly once in the result, at hop 2 (its
       first-seen hop), not twice.
     - `NilGraph` — `g == nil`: empty result, no panic, no error (or documented behavior —
       decide during implementation and keep test/doc comment consistent).
3. Run `gofmt -l`, `go vet ./...`, `go build ./...`, then
   `go test ./engine/graph/... -race -run TestGraphNeighbors -timeout 120s`, then the full
   package suite `go test ./engine/graph/... -race -timeout 180s` to confirm no regression
   (self-consistency only, not verification).
4. One local commit: `feat: add GraphNeighbors BFS traversal API with type filter and cap (issue #15, 3.1.5)`.
