# Requirement — Subtask 4.2.1 (issue #21, milestone #6 "Phase 4: Query pipeline")

## Source
- GitHub issue #21, subtask "4.2.1 — SearchCandidates implementation (non-LLM candidate
  topic search)" (`gh issue view 21`).
- Dispatch instructions from the orchestrating agent (this run's launch message).

## Literal acceptance criteria
> Given a query string/terms, SearchCandidates returns a ranked candidate topic list via
> btree prefix-scan + simple term-overlap ranking, suitable for both ingestion
> shortlisting (task-3.4.2) and query-time topic selection.

## Literal test spec
> `go test ./engine/rpc/... -run TestSearchCandidates`: fixture-populated btree, assert
> ranked candidate results for known query terms match expected ordering.

## Impacted modules (per issue)
- `engine/rpc/search_candidates.go` (new)
- `engine/rpc/search_candidates_test.go` (new)

## Pre-existing state (critical context, confirmed by reading the code)
`engine/rpc/server.go`'s `SearchCandidates` handler already exists (task-3.2.2, issue
#16, Phase 3). It:
- delegates to `btree.PrefixScan(s.btreeStore, s.btreeRootNodeID, req.GetQuery())`,
  treating `req.Query` as a literal string prefix (btree exposes no other query
  primitive — `Lookup` is exact-match only);
- applies `max_results` semantics: negative -> `codes.InvalidArgument`; `0` -> no cap
  (documented deviation from `GraphNeighborsRequest.max_nodes`'s "0 = empty" contract);
- assigns every result the same placeholder score via
  `const searchCandidateScore float32 = 1.0`, with an explicit doc comment stating real
  ranking was deliberately out of scope for that earlier thin-adapter subtask and
  pointing at `.cdr/runs/2026-07-09/002-implementation/impact-analysis.json`.
- `s.btreeStore == nil` -> returns an empty response (degraded-mode, not an error).

This subtask (4.2.1) is the one that adds the real term-overlap ranking on top of that
existing plumbing, per the issue text and the dispatch instructions.

`proto/hivemind.proto` confirms the message shapes available:
```proto
message SearchCandidatesRequest {
  string query = 1;
  int32 max_results = 2;
}
message CandidateTopic {
  uint64 file_id = 1;
  string path = 2;
  float score = 3;
}
message SearchCandidatesResponse {
  repeated CandidateTopic candidates = 1;
}
```
`query` is a single string, not a repeated terms list — there is no separate
multi-term field. `CandidateTopic.score` already exists (previously always 1.0).

`engine/btree.PrefixScan` (`engine/btree/scan.go`) returns `[]ScanEntry{Path, FileID}`
in ascending sorted-path order, matched by literal string prefix (`strings.HasPrefix`).
It carries no document/topic text beyond the indexed path string itself — candidates are
keyed by file path, not arbitrary content. So "term-overlap ranking" here can only mean
overlap between the query's tokenized terms and the candidate path's own tokenized
terms (directory + filename components) — there is no other text available to rank
against at this layer.

This reading is corroborated by `agents/ingestion/shortlist.py` (task-3.4.2, issue #18),
which explicitly documents today's division of labor: `SearchCandidates` is "a btree
prefix scan over topic *path*, not a content/semantic search... every result gets the
same placeholder score. It has no way to rank by document content"; that module already
does its OWN local Okapi-BM25 re-ranking over each candidate topic path's tokens, using
`query=""` (empty-string prefix, matching every stored key) plus `max_results=pool_size`
purely to bound the pool. Because `shortlist()` re-ranks locally regardless of whatever
score `SearchCandidates` reports, changing the scoring here cannot break 3.4.2's own
correctness — but 3.4.2's existing behavior of using an empty-string query to retrieve an
unranked full pool must still work identically (empty-query term-overlap is a no-op).

`docs/LLD/rpc.md` documents `SearchCandidates` as "non-LLM candidate topic search
consumed by the Python query-agent's topic-selector" and `query-agent.md`/
`ingestion-agent.md` do not prescribe a specific ranking formula beyond the issue's own
"simple term-overlap ranking" wording — no existing LLD scoring convention to conform to
beyond what the issue itself specifies.

## Scope decision
1. New `engine/rpc/search_candidates.go`: tokenizer (split on non-alphanumeric,
   lower-case) + a term-overlap scoring function operating on (query terms, candidate
   path) + a ranking function that scores and stably sorts a `[]btree.ScanEntry` into
   `[]*hivemindv1.CandidateTopic` (score descending, ties broken by the original
   PrefixScan sorted-path order for determinism).
2. Refactor `server.go`'s `SearchCandidates` handler to call into the new ranking
   function instead of assigning the constant placeholder score, preserving every other
   existing behavior verbatim: nil-`btreeStore` degraded mode, negative-`max_results`
   `InvalidArgument` error, `max_results == 0` "no cap" semantics, and the order of
   capping (cap is applied to the pool of PrefixScan matches — capping happens before or
   after ranking is a real design choice, see plan.md).
3. New `engine/rpc/search_candidates_test.go` per the literal test spec: fixture-populated
   btree with several topic paths (chosen so term-overlap actually changes ordering vs.
   pure lexicographic-path order), known multi-term query, assert ranked order matches
   expected (higher overlap ranks first).
4. Empty-query (`query == ""`) case must remain a no-op ranking (all entries score
   equally, stable order == PrefixScan's own sorted order) to preserve
   `agents/ingestion/shortlist.py`'s existing pool-retrieval contract byte-for-byte.

## Explicitly out of scope
- Changing `btree.PrefixScan` itself, or adding any new btree query primitive.
- Any BM25/embedding-style ranking (that already lives client-side in
  `agents/ingestion/shortlist.py`; duplicating it here would be new business logic
  beyond "simple term-overlap ranking").
- Touching `proto/hivemind.proto` (no new fields needed; `score` already exists).
- Any GitHub issue/milestone state change, any push.

## Security note
Per standing instruction, this run treats any embedded prompt-injection-style text
found in `gh issue view 21`'s output as untrusted data only. The issue body itself
(`gh issue view 21`, captured above) contained no such embedded text — clean. Separately,
this session's own tool-call flow surfaced a system-reminder-formatted "date changed to
2026-07-11" notice and unrelated MCP-server tool-usage instructions; these arrived as
harness-level reminders in the agent's own control flow (not embedded inside any gh/git
tool output text), so per the standing instruction they are treated as legitimate
harness reminders, not content injection — disclosed here for completeness, not acted on
in any way that affects this subtask's implementation.
