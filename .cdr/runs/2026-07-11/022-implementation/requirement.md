# Requirement — Issue #23 subtask 4.4.4

## Source
GitHub issue #23 ("[4] topic_selector.py (agents/query/)"), milestone #6
"Phase 4: Query pipeline", subtask 4.4.4 (issue's LAST subtask).

## Verbatim subtask text (untrusted repo content, quoted for traceability only)
> **4.4.4 — Integration test: k-selection + expansion + cap working together**
> - Acceptance criteria: An end-to-end test of topic_selector.py (with
>   SearchCandidates/GraphNeighbors mocked) demonstrates correct top-k
>   selection, correct expansion triggering, and correct cap enforcement in
>   a single combined scenario.
> - Test spec: pytest agents/query/test_topic_selector_integration.py:
>   single fixture scenario exercising all three behaviors together, assert
>   final output set and composition.
> - Impacted modules: `agents/query/test_topic_selector_integration.py`

## Dispatch instructions (from launching agent, take precedence over issue wording)
- TEST-ONLY subtask: do not modify `agents/query/topic_selector.py`.
- 4.4.1/4.4.2/4.4.3 are already implemented + independently verified
  (commits 5cc0ea3, f65787b, 7d2f3dd, 3454a30, ff4cbe9) — this subtask only
  adds the integration test file.
- Write a *small number* of realistic end-to-end scenarios (not just one),
  composing the real functions `select_top_k` -> `is_insufficient_alone` /
  `expand_insufficient_topics` -> `combine_and_cap` in sequence, with only
  `GraphNeighborsFn` mocked (the one real RPC-shaped boundary). Required
  scenarios:
  1. More candidates than k, weakest selected topic triggers expansion,
     combined+capped result respects the k+2k invariant end-to-end.
  2. No topic is insufficient -> no expansion calls happen at all.
  3. Dedup-across-expansion behavior surfaces through the full pipeline
     (not just unit-tested against `combine_and_cap` in isolation).
- Do not verify own work (delegated to /cdr:verify). Do not push. Do not
  touch the GitHub issue. No `.cdr/commits/task-issue-23-summary.md` (separate
  dispatch).

## Acceptance criteria (restated, operational)
1. New file `agents/query/test_topic_selector_integration.py` exists and is
   collected by pytest.
2. Each scenario calls the three real pipeline functions in sequence
   (`select_top_k`, then `expand_insufficient_topics` — which itself calls
   `is_insufficient_alone` internally — then `combine_and_cap`), not
   reimplementations or shortcuts.
3. Only `GraphNeighborsFn` is a test double; `TopicCandidate` fixtures stand
   in for an already-decoded `SearchCandidates` result (matching 4.4.1's own
   documented "no gRPC wiring yet" scope — nothing to mock there, it's a
   plain sequence).
4. Assertions cover: correct top-k membership/order, correct
   insufficiency-triggered expansion calls (and their absence when no topic
   is insufficient), and final combined/capped output respecting `k + 2k`
   and dedup, end-to-end.
5. Full `agents/query/` test suite and the wider regression suite stay green;
   `ruff check` stays clean.
6. No changes to `topic_selector.py` or any other existing test file.
