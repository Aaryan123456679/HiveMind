# Architecture discovery — Subtask 4.2.1

## Index trail followed (per CDR token-order protocol)
1. `.cdr/index/file.jsonl`, `.cdr/index/feature.jsonl`, `.cdr/index/task.jsonl` — grepped
   for `SearchCandidates`/`btree`/`rpc/server` before opening any source.
2. `docs/LLD/rpc.md` — grepped for `rank`/`score`/`SearchCandidate` mentions.
3. Targeted source reads: `engine/rpc/server.go` (SearchCandidates handler + its doc
   comments + Server struct field docs for `btreeStore`/`btreeRootNodeID`),
   `proto/hivemind.proto` (message shapes), `engine/btree/scan.go` (`PrefixScan`/
   `ScanEntry`), `engine/rpc/server_test.go` (existing `SearchCandidates` subtests +
   fixture-building helpers), `agents/ingestion/shortlist.py` (task-3.4.2's consumer,
   for the documented RPC/local-ranking division of labor).

## Key findings
- `task.jsonl` entry for `task-3.2.2` and `task-3.4.2` both independently confirm, from
  two different completed CDR runs, that `SearchCandidates`'s score is a constant
  placeholder and real ranking does not exist anywhere in the Go engine yet — matches
  the dispatch instructions' framing exactly, no surprises.
- `engine/rpc/server_test.go`'s existing fixture (`newFixture`) seeds exactly two btree
  paths (`topics/alpha/intro`, `topics/beta/intro`) and drives 3 `SearchCandidates`
  subtests (basic match, max-results cap, no-matches). None of them depend on a specific
  relative order among >1 same-prefix result beyond count, so introducing term-overlap
  ranking cannot regress them (confirmed by re-deriving expected results below in
  validation-matrix.json). New ranking-order assertions belong in the new
  `search_candidates_test.go` file per the issue's literal file-list, not by modifying
  the existing file.
- `btree.PrefixScan` signature: `func PrefixScan(store *NodeStore, rootNodeID uint64,
  prefix string) ([]ScanEntry, error)`, `ScanEntry{Path string, FileID uint64}`. No
  other text/content field exists per entry — path is the only string available to
  tokenize.
- `Server.btreeStore`/`Server.btreeRootNodeID` are the only fields `SearchCandidates`
  touches; `NewServer`'s constructor signature does not need to change for this
  subtask (no new dependency required — term-overlap ranking is pure computation over
  already-available data, no new storage engine primitive).
- No `sort`/`strings`/`regexp` imports currently exist in `server.go`; keeping the new
  logic in its own file (`search_candidates.go`) avoids growing `server.go`'s import
  block, matching the issue's literal file split.
- `docs/LLD/rpc.md` has no scoring-formula convention beyond what the issue itself says
  ("simple term-overlap ranking") — nothing to reconcile against a pre-existing LLD
  design note.

## Design decision: where capping and ranking compose
Current handler order is: PrefixScan -> cap to `max_results` -> build `CandidateTopic`s
(constant score). If ranking now changes relative order, capping BEFORE ranking could
silently drop a better-scoring match that happened to sort later in raw PrefixScan
order (e.g. lexicographically later path but a higher term overlap). Decision: rank
first (score + stable-sort the full PrefixScan result set), THEN cap to `max_results`.
This is a strict quality improvement consistent with "ranked candidate topic list" in
the acceptance criteria (the doc explicitly says results should be *ranked*, and a
correct top-K ranked list requires ranking before truncating) and does not change
behavior for the empty-query case (score is a ranking no-op there, so cap-after-rank
degenerates to the previous cap-of-raw-PrefixScan-order behavior, preserving
`shortlist()`'s existing pool contract).

## Files touched
- New: `engine/rpc/search_candidates.go` — tokenizer, term-overlap scorer, ranking
  function.
- New: `engine/rpc/search_candidates_test.go` — `TestSearchCandidates` per literal spec.
- Modified: `engine/rpc/server.go` — `SearchCandidates` handler body only, reusing the
  new ranking function; existing error handling / nil-store / max_results-cap-timing
  reordered as decided above; `searchCandidateScore` constant and its doc comment
  removed since it's no longer used (real scores now computed).
