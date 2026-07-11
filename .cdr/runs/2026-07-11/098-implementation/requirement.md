# Requirement -- subtask 4.5.9.1 (issue #47, milestone #10)

Source: GitHub issue #47 ("[4.5] engine/rpc: SearchCandidates only pools first-token-prefix-
matching candidates (query-time topic-selection limitation)"), confirmed directly via
`gh issue view 47`.

Epic: Phase 4.5: Storage-engine technical debt & correctness follow-ups.

Impacted modules named by the issue: `engine/rpc/search_candidates.go`, `engine/btree/`,
`agents/query/topic_selector.py` (consumer).

Source of the finding: `.cdr/memory/pending.md` (top entry) and
`.cdr/index/regression.jsonl` (subtask 4.2.1, issue #21, commit `b8ebc64`, tagged
`design_limitation`/`non_blocking`).

## Problem (as stated in the issue)

`engine/rpc/search_candidates.go`'s term-overlap ranking (task 4.2.1, issue #21) delegates
candidate-pool selection entirely to `btree.PrefixScan`, which only supports literal-prefix
matching. The candidate pool is therefore restricted to paths whose leading bytes match the
query string's first whitespace token, before any term-overlap ranking ever runs. A realistic
multi-word natural-language query (e.g. "how do I configure the graph database") prefix-scans
on "how" and will likely return zero candidates. Confirmed backward-compatible with
`agents/ingestion/shortlist.py` (always passes `query=""`), but a real limitation for any
caller passing a genuine natural-language query -- including the now-built
`agents/query/topic_selector.py` / `intent_refiner.py` pipeline (issues #22/#23), not confirmed
to have addressed this gap. Architecturally forced by `btree`'s lack of any non-prefix query
primitive; out of scope for 4.2.1.

## This subtask (4.5.9.1) -- scope: decide + document ONLY, no code changes

Acceptance criteria (per the issue body):
- An explicit decision recorded in `docs/LLD/btree.md` and/or `docs/LLD/query-agent.md`,
  choosing among:
  - (a) extend `engine/btree` with a non-prefix query primitive (e.g. token-set intersection)
  - (b) have callers issue multiple single-term `PrefixScan`s and merge/re-rank client-side
  - (c) formally accept and document the prefix-only limitation as a known constraint
- The decision must be justified against `agents/query/topic_selector.py`'s actual current
  calling pattern (read directly, not assumed).
- Doc-only change; test spec for the *separate*, deferred subtask 4.5.9.2 is
  `./engine/rpc/... -run TestSearchCandidatesMultiWordQuery` (multi-word query returns
  non-empty, correctly-ranked results including at least one path not prefix-matching the
  first token) against impacted modules `engine/rpc/search_candidates.go`,
  `engine/rpc/search_candidates_test.go`, `engine/btree/scan.go`. No automated test is
  required for 4.5.9.1 alone (doc-only).
- 4.5.9.2 (the actual code implementation of whichever option is chosen) is explicitly OUT OF
  SCOPE for this dispatch -- deferred to a later, separate dispatch.

## Additional context to confirm independently (per dispatching instructions)

- `agents/query/topic_selector.py`'s actual current behavior, read directly.
- Issue #56 (milestone #10, "agents/query + api: real gRPC/HTTP wiring for /query route"),
  running concurrently this session on different files, to check for any overlap/interaction
  with this decision's content.
