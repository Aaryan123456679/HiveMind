# Requirement (fix-cycle attempt 2/3, issue #47 subtask 4.5.9.2)

Source: `gh issue view 47` (pulled fresh) + CHANGES_REQUESTED verdict at
`.cdr/runs/2026-07-11/110-verification/verification.json` (commit `8c3c7a8` on
`origin/main`).

## Blocking finding

`perTermPoolCap`/`mergedPoolCap` (`engine/rpc/search_candidates.go`) truncate the slice
`btree.PrefixScan` returns, but `PrefixScan` (`engine/btree/scan.go`) already performs the
FULL leaf-chain traversal before returning -- so the existing caps bound retained pool
memory, not scan cost. Additional gaps identified by verification:

- No bound on the NUMBER of distinct terms `candidatePool`'s loop processes.
- No de-duplication of repeated terms -- a query repeating one term N times triggers N
  redundant full-cost `PrefixScan` calls (the `mergedPoolCap` early-break cannot catch this,
  since a repeat contributes zero new entries to the deduplicated merge).
- `SearchCandidatesRequest.Query` has no term-count validation in `server.go`.
- `docs/LLD/query-agent.md` / `docs/LLD/btree.md` overclaim these caps "resolve"/"bound
  worst-case fan-out cost".

## Chosen fix (option (a), favored by the requirement)

1. `dedupTerms` -- de-duplicate `candidatePool`'s term list before the scan loop.
2. `maxQueryTerms` (32) + `validateQueryTermCount` -- reject (not silently truncate) a
   query with more than 32 distinct terms, enforced in `SearchCandidates` (`server.go`)
   BEFORE `candidatePool` issues any `PrefixScan` call, via `codes.InvalidArgument`.
3. Correct `perTermPoolCap`/`mergedPoolCap` doc comments and both LLD docs to accurately
   state they bound retained memory only, and point to `dedupTerms`/`maxQueryTerms` as the
   actual scan-cost bound.
4. Add regression tests exercising the new bound: `TestDedupTermsCollapsesRepeatedTerms`,
   `TestSearchCandidatesRepeatedTermScansOnce`, `TestSearchCandidatesRejectsTooManyDistinctQueryTerms`.

Per invariant I4, this agent does not verify its own work -- handed off to `/cdr:verify`.
