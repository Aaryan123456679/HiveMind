# Issue #21 (milestone #6, "Phase 4: Query pipeline") -- consolidated closure summary

Issue #21 comprises exactly 1 subtask. It is now independently implemented,
verified, and committed **locally only**.

| Subtask | Summary | Commit | Verdict | Verification run |
|---|---|---|---|---|
| 4.2.1 | `engine/rpc/search_candidates.go` (`tokenizeTerms`/`termOverlapScore`/`rankCandidates`) -- `SearchCandidates` now computes real term-overlap ranking scores over the `btree.PrefixScan` candidate pool, replacing the prior constant placeholder. `engine/rpc/server.go` updated to call the new ranking before applying `max_results` capping. | `b8ebc6404d5dfa7bea5d7b0979d92de7e95c2702` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/009-verification/verification.json` |

## Impact

Issue #21 gives `SearchCandidates` a real, order-sensitive ranking signal
(case-insensitive term overlap between the query and each candidate path,
scored as `matched / len(queryTermSet)`) in place of the previous constant
placeholder score, with ties broken by the stable sort preserving
`PrefixScan`'s original path order. `max_results` capping is applied *after*
ranking, not before, so capping and relevance-ordering interact correctly.
No new `btree` primitive was added or required -- the implementation is
scoped entirely to `engine/rpc`, matching the issue's file-list scope
(`search_candidates.go`/`_test.go` new, `server.go` modified only). No
existing production caller's behavior changes: `agents/ingestion/shortlist.py`
is confirmed to be the only current caller, and it always passes an
empty-string query, for which the new ranking is a verified no-op that
degenerates back to plain `PrefixScan` order.

## Verification

- Verdict: `PASS_WITH_COMMENTS`, commit `b8ebc6404d5dfa7bea5d7b0979d92de7e95c2702`,
  run `.cdr/runs/2026-07-11/009-verification/verification.json`.
- `requirements_conformance`, `architecture_conformance`,
  `high_scrutiny_finding_a_backward_compat` (verified_true),
  `high_scrutiny_finding_c_scoring_correctness` (verified_correct),
  `regression_risk`, `edge_cases`, `security`, `maintainability`,
  `test_coverage`: all `pass`.
- `performance`: `pass_with_comment` (O(n log n) sort + O(n * avg_path_len)
  tokenization per call over the bounded `PrefixScan` pool; acceptable at
  the pool sizes in use today, e.g. `pool_size=200` in `shortlist.py`).
- `go build ./engine/...`, `go vet ./engine/...` both exit 0; `gofmt -l` on
  the 3 touched/added files: clean. `go test ./engine/... -count=1` (full
  suite, no cache): all packages pass, including `engine/rpc` (22.752s).
  Pre-existing `SearchCandidates` subtests in `server_test.go`/
  `integration_test.go` untouched and green.
- No blocking findings (`blocking_findings: []`).
- No embedded prompt-injection-style text found in the issue/diff/commit
  content reviewed by the verifier.

### Non-blocking finding carried forward (verbatim from verification.json)

- **`high_scrutiny_finding_b_query_time_gap`** (status: `real_but_scoped_limitation`):
  "Confirmed materially: only entries whose PATH literally starts with the
  query's FIRST whitespace token ever enter the candidate pool (PrefixScan
  is a literal-prefix scan on the full path string, not per-token). A
  natural-language query like 'how do I configure the graph database' would
  PrefixScan on 'how', almost certainly matching zero stored paths,
  returning an empty ranked list regardless of true relevance. The added
  test itself has to contrive fixtures where every path is prefixed with
  the query's first token to demonstrate ranking at all -- this is not
  hypothetical, it's structurally required for the ranking to ever
  activate. This is an architecturally-forced limitation (btree exposes no
  full/inverted scan, and extending btree is explicitly out of this
  subtask's scope per the issue's file list), reasonably disclosed in code
  comments, but the acceptance criteria's 'suitable for ... query-time
  topic selection' is only partially met -- future Phase 4 query-time work
  will need a different pool-selection strategy (e.g., multi-prefix union
  across all query terms, or a fallback full scan) before this RPC is
  actually usable for realistic natural-language queries. Flagged as a
  design comment, not a regression or a blocker for this subtask."
- Related `non_blocking_findings` entries from the same run: "Query-time
  topic selection (future Phase 4 work) will need a pool-selection strategy
  beyond single-first-token literal prefix before natural-language queries
  are usable; recommend a follow-up issue." and "No test exercises the case
  where the query's first token has zero PrefixScan matches (returns empty
  list even when other terms would otherwise overlap); would make the known
  limitation explicit in test suite."

**Flag for the not-yet-built query-time selector (issues #22/#23):** this
limitation is architecturally forced by `btree.PrefixScan` having no other
query primitive, and is confirmed backward-compatible with today's only
real caller (`agents/ingestion/shortlist.py`, always `query=""`). It must be
explicitly addressed -- not silently inherited -- when issue #23's
`topic_selector.py` (or issue #22's `intent_refiner.py`, if it calls
`SearchCandidates` directly) is built: either (a) extend `btree` with a
non-prefix query primitive, (b) have the caller issue multiple single-term
`PrefixScan`s and merge/re-rank client-side, or (c) explicitly accept and
document the limitation in `docs/LLD`. Tracked in `.cdr/memory/pending.md`
and `.cdr/index/regression.jsonl`; no dedicated GitHub issue exists yet --
address in context when #22/#23 are built, per standing convention, rather
than as a standalone milestone item.

## Release notes

- `SearchCandidates` (`engine/rpc`) now ranks its candidate pool by real
  case-insensitive term overlap with the query instead of returning a
  constant placeholder score, with `max_results` applied after ranking.
- No behavior change for the current caller (`agents/ingestion/shortlist.py`,
  always `query=""`); this is a pure enhancement to the RPC's ranking
  quality for future callers that pass a real query string.
- Known limitation, not yet a defect: multi-word natural-language queries
  are only useful today if their first whitespace-delimited token is a
  literal path prefix of the desired results, due to `btree.PrefixScan`
  having no other query primitive. Must be revisited before issue #23's
  query-time topic selector is built on top of this RPC.

This closes out issue #21's single subtask. Ready to close pending separate,
fresh user authorization for push/close (this workflow is local-commit only
and does not push or touch the GitHub issue).
