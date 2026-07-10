# Plan — Subtask 4.2.1

## 1. `engine/rpc/search_candidates.go` (new)
- Package doc comment cross-referencing this run's requirement.md/architecture-discovery.md
  and the pre-existing task-3.2.2 scope-reduction it completes.
- `termSplitRE = regexp.MustCompile(`[^a-zA-Z0-9]+`)` — same simple tokenization
  convention already used by `agents/ingestion/shortlist.py`'s `_TOKEN_SPLIT_RE`
  (non-alphanumeric run as separator), kept consistent across the Go/Python boundary
  even though the two are independent implementations.
- `tokenizeTerms(s string) []string`: lower-cases `s`, splits on `termSplitRE`, drops
  empty strings, returns the term list (duplicates kept — overlap scoring dedupes via a
  set built from the query side only, matching "simple term-overlap" semantics: how many
  distinct query terms appear anywhere in the path's term set).
- `termOverlapScore(queryTerms []string, path string) float32`: builds a `map[string]struct{}`
  from `tokenizeTerms(path)`; counts how many of the (deduplicated) `queryTerms` are present
  in that set; returns `float32(matched) / float32(len(uniqueQueryTerms))` (normalized to
  [0,1]), or `0` if `queryTerms` is empty (documented no-op case).
- `rankCandidates(query string, entries []btree.ScanEntry) []*hivemindv1.CandidateTopic`:
  - `queryTerms := tokenizeTerms(query)`.
  - Build one `*hivemindv1.CandidateTopic` per entry with `Score:
    termOverlapScore(queryTerms, e.Path)`.
  - `sort.SliceStable` descending by `Score` (stable preserves PrefixScan's original
    ascending-path order as the tie-break, which is also the empty-query no-op case since
    all scores are then 0).
  - Return the sorted slice (uncapped — capping is the caller's job, per the
    architecture-discovery.md rank-then-cap decision).

## 2. `engine/rpc/server.go` — `SearchCandidates` handler
- Keep: nil-`btreeStore` early return, `max_results < 0` -> `InvalidArgument`,
  `PrefixScan` call + `Internal` error wrap.
- Replace the placeholder-score candidate-building loop with:
  ```go
  candidates := rankCandidates(req.GetQuery(), entries)
  if maxResults > 0 && len(candidates) > maxResults {
      candidates = candidates[:maxResults]
  }
  return &hivemindv1.SearchCandidatesResponse{Candidates: candidates}, nil
  ```
- Remove `const searchCandidateScore float32 = 1.0` and its doc comment (dead code after
  this change) plus rewrite the handler's own doc comment to describe the new real
  ranking instead of "why score is a constant placeholder."

## 3. `engine/rpc/search_candidates_test.go` (new)
- `TestSearchCandidates`: build a minimal in-memory btree fixture (reusing the same
  `btree.NodeStore`/`btree.Insert` pattern already used in `server_test.go`'s
  `newFixture`, but self-contained in this new file, not sharing test state) seeded with
  paths chosen so that pure lexicographic order and term-overlap order diverge, e.g.:
  - `"docs/alpha/graph-theory"`
  - `"docs/beta/graph-database"`
  - `"docs/gamma/unrelated-topic"`
  - query `"graph database"` -> expect `docs/beta/graph-database` (2/2 terms) ranked
    strictly ahead of `docs/alpha/graph-theory` (1/2 terms) ranked ahead of
    `docs/gamma/unrelated-topic` (0/2 terms).
  - Construct a `*rpc.Server` via `NewServer` with only `btreeStore`/`btreeRootNodeID`
    populated (catalog/content-store/idAlloc are required non-nil per `NewServer`'s
    contract — supply minimal real fixtures for those the same way `server_test.go` does,
    not mocks) and call `SearchCandidates` end-to-end, asserting `resp.Candidates`' path
    order exactly matches the expected ranking (not just membership).
  - Additional subtest: empty query (`Query: ""`) returns candidates in the same order
    `btree.PrefixScan` itself would (no-op ranking), preserving `shortlist()`'s pool
    contract.
  - Additional subtest: `MaxResults` caps the RANKED list (top-K), not an arbitrary
    subset of PrefixScan's raw order — assert the single returned candidate is the
    highest-scoring one, not just any one.

## 4. Validation / self-consistency
- `go build ./engine/...`
- `go test ./engine/rpc/... -run TestSearchCandidates -v` (targeted)
- `go test ./engine/... -v` (full regression, confirm existing SearchCandidates
  subtests in server_test.go and integration_test.go stay green)
- `go vet ./engine/...`
- `gofmt -l engine/rpc/search_candidates.go engine/rpc/search_candidates_test.go engine/rpc/server.go`

## 5. Commit
- One local commit, Problem/Solution/Impact style, no push, no GitHub issue touch.
