# Plan — 4.4.3

## Interface design (disclosed choice — LLD names no function)

`docs/LLD/query-agent.md` and the issue both describe the *invariant* ("combined result hard-capped
at k + 2k total files") but name no function. Following 4.4.1/4.4.2's own precedent (plain free
functions, module-level `DEFAULT_*` constants, frozen dataclasses decoupled from gRPC types), this
subtask adds:

```python
def combine_and_cap(
    selected: Sequence[TopicCandidate],
    expansions: Sequence[ExpansionResult],
    *,
    k: int = DEFAULT_K,
) -> list[int]:
    """Return the final, deduplicated, hard-capped list of file_ids."""
```

Name chosen: `combine_and_cap` (matches the dispatch's own suggested name; clearly names both of its
two responsibilities — combining the two sources and enforcing the cap — and reads naturally next to
`select_top_k`/`expand_insufficient_topics` in the same module).

**Return type is `list[int]` (file_ids), not `list[TopicCandidate]`.** Rationale: `selected` yields
`TopicCandidate` (which has `path`/`score`), but `ExpansionResult.neighbors` yields `GraphNeighbor`
(which has `edge_type`/`weight`/`hop`, no `path`/`score`). There is no common dataclass shape between
the two sources that would let the function return a single strongly-typed list of "the same kind of
thing" — the only field both `TopicCandidate` and `GraphNeighbor` share is `file_id`. The issue's own
acceptance criteria and test spec talk purely in terms of a file-count invariant ("final result
length"), not in terms of preserved per-item metadata. So the function's contract is: produce the
final *set of files* (by id) the pipeline will fetch/include as context; a later subtask (out of
scope here, likely 4.4.4 or the synthesizer wiring) is responsible for mapping file_ids back to
actual file content when assembling the synthesizer prompt. This is called out explicitly in the
docstring as a disclosed scope boundary, mirroring how 4.4.1/4.4.2 disclosed their own scope
boundaries.

## Cap formula

`cap = k + 2 * k` where `k` is the function's own `k` keyword parameter (defaulting to `DEFAULT_K`,
imported/reused, not re-defined) — matching the LLD's exact `k + 2k` wording and matching 4.4.1's
`k` parameter precedent (same name, same default, so a caller passing a custom `k` to `select_top_k`
can pass the identical `k` to `combine_and_cap` for a consistent cap). No new `DEFAULT_CAP_MULTIPLIER`
constant is introduced since the issue/LLD specify the multiplier (`+2k`) as fixed, not tunable —
inventing a tunable multiplier constant would be unfounded speculation beyond this subtask's scope,
mirroring the "disclosed choice, not over-engineering" style of the two prior subtasks. Validation:
`k < 0` raises `ValueError` (matching `select_top_k`'s own `k` validation, for consistency).

## Dedup + ordering (see architecture-discovery.md for full reasoning)

1. Walk `selected` in the order given (already descending-score order from `select_top_k`), collect
   each `file_id` into the result if not already present.
2. Walk `expansions` in the order given (== `expand_insufficient_topics`'s own per-topic order), and
   within each `ExpansionResult`, walk `.neighbors` in the order given (engine's own ordering,
   untouched). Collect each neighbor `file_id` into the result if not already present (whether the
   duplicate is against a previously-collected selected file_id or a previously-collected neighbor
   file_id from an earlier expansion).
3. Truncate the deduplicated, order-preserved list to the first `k + 2k` entries.

This guarantees: (a) top-k selections are never displaced by expansion neighbors when both fit,
because they are always collected first; (b) a file appearing in multiple sources counts once,
per "final selected-file set" wording; (c) `len(result) == min(len(deduplicated_available), k + 2k)`
exactly matching the issue's test-spec formula, where "available" is read as "the number of
*distinct* file_ids offered across both sources" (post-dedup), since the pre-dedup count is not
what's meaningful for a *file* cap.

## Edge cases planned for
- Empty `selected` and empty `expansions` -> `[]`.
- `expansions` with empty `.neighbors` lists -> contributes nothing, no crash.
- Duplicate file_id within `selected` itself (should not occur per `select_top_k`'s contract, but
  handled safely: second occurrence dropped, does not double-count against the cap).
- Duplicate file_id across two different `ExpansionResult`s' neighbor lists (two different insufficient
  topics both graph-neighboring the same file) -> counted once.
- Duplicate file_id where a neighbor's file_id equals one of the *selected* topics' own file_id (a
  topic's own file surfacing again as someone else's graph neighbor) -> counted once, already covered
  by step 1 running first.
- Oversized combined pool (more distinct files available than `k + 2k`) -> truncated exactly to
  `k + 2k`, per issue's explicit test-spec example (k=3 -> cap of 9).
- `k=0` -> cap is `0`, result is always `[]` regardless of input size (consistent with `select_top_k`'s
  own `k=0` -> `[]` behavior).
- Negative `k` -> `ValueError`, consistent with `select_top_k`'s own validation.

## Test file plan (`test_topic_selector_cap.py`)
Mirrors the two existing test files' style (module docstring citing issue #23 4.4.3's exact test-spec
wording, `from __future__ import annotations`, plain `TopicCandidate`/`GraphNeighbor`/`ExpansionResult`
fixtures, no mocking needed since `combine_and_cap` takes no injected callable).

1. `test_combine_and_cap_oversized_pool_truncated_to_k_plus_2k` — the issue's literal test spec:
   feed an oversized candidate+expansion set (more than k+2k distinct files, e.g. k=3, 15 available
   distinct files across selected+expansions), assert `len(result) == min(available, k + 2*k)` and
   assert it equals exactly `9` for this fixture.
2. `test_combine_and_cap_under_cap_returns_all_available` — fewer distinct files available than the
   cap -> all returned, dedup applied, length == available (not the cap).
3. `test_combine_and_cap_dedups_file_reachable_both_ways` — a file_id appears both in `selected` and
   in one *other* topic's expansion neighbors -> counted once in the result.
4. `test_combine_and_cap_dedups_across_two_expansions` — same neighbor file_id returned by two
   different `ExpansionResult`s -> counted once.
5. `test_combine_and_cap_prioritizes_selected_over_expansion_when_truncating` — construct a case where
   without dedup-priority, an expansion neighbor would displace a directly-selected topic's file;
   assert all `selected` file_ids survive the cap and the truncation only ever drops expansion-neighbor
   ids.
6. `test_combine_and_cap_empty_inputs_return_empty_list`.
7. `test_combine_and_cap_default_k_matches_default_k_constant` — default cap == `DEFAULT_K + 2 *
   DEFAULT_K` == 9.
8. `test_combine_and_cap_rejects_negative_k`.
9. `test_combine_and_cap_k_zero_returns_empty_list`.
10. `test_combine_and_cap_preserves_order` — result order is selected-first (score-descending), then
    expansion order, for a small non-truncated case.

## Self-consistency plan (NOT verification — internal sanity only)
- `cd agents && python3 -m pytest query/ -q` — new + existing query tests green.
- `cd agents && python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q` — full regression;
  expect the same 2 pre-existing unrelated failures already logged in 019-verification (protobuf
  gencode mismatch in `ingestion/test_shortlist.py`, issue #46) and nothing new.
- `ruff check` over the two touched/created files.
- Validation matrix: every planned test case actually present and passing.
