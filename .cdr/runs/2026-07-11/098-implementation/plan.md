# Plan -- subtask 4.5.9.1

1. Confirm issue #47's real acceptance criteria via `gh issue view 47` (done).
2. Read `agents/query/topic_selector.py` directly to establish its actual current
   `SearchCandidates`-calling pattern (done -- it does not call it at all yet).
3. Read `engine/rpc/search_candidates.go` directly to confirm exactly where the first-token-only
   restriction lives (`prefixTerm`) and confirm the ranking layer (`rankCandidates`/
   `termOverlapScore`) already generalizes to a multi-term pool (done).
4. Read `docs/LLD/` directory listing and both candidate target docs (`btree.md`,
   `query-agent.md`) plus `rpc.md` for cross-reference accuracy (done).
5. Check issue #56 for scope/file overlap (done -- no file overlap, but material context for
   *why* the decision matters now).
6. Decide among options (a)/(b)/(c), justified against step 2/3's findings -- decision: (b).
7. Record the decision:
   - `docs/LLD/btree.md`: short "Known risks" bullet + pointer to the full rationale.
   - `docs/LLD/query-agent.md`: full decision writeup (option, rationale, residual limitation,
     pointer to deferred 4.5.9.2 impacted modules).
8. Self-consistency check: re-read both edited docs for internal consistency and accuracy
   against the source files re-read in step 2/3 (no build/test applicable -- doc-only change).
9. `git diff --stat` to confirm only the two intended files changed, then ONE local commit
   (Problem/Solution/Impact format), no push.
10. Write validation-matrix.md, self-consistency.md, handoff.json; finalize metadata.json.

Explicitly not planned: any change to `engine/rpc/search_candidates.go`,
`engine/rpc/search_candidates_test.go`, or `engine/btree/scan.go` (subtask 4.5.9.2, deferred).
