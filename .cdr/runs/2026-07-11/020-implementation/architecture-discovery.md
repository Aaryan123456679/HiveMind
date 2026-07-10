# Architecture Discovery — 4.4.3

## Token order followed
1. `.cdr/index/` (none directly indexing this file by symbol; run history under `.cdr/runs/2026-07-11/`
   for prior 4.4.1/4.4.2 runs consulted instead, since this is the same file being extended a third
   time in the same day's run sequence).
2. Prior handoffs: `.cdr/runs/2026-07-11/*-implementation/handoff.json` for 4.4.1
   (commit `5cc0ea3`) and 4.4.2 (commit `f65787b`/`7d2f3dd`), plus the 4.4.2 verification verdict
   (`019-verification/*.json`, PASS_WITH_COMMENTS) — read above, in the parent turn, before any
   source file read.
3. Targeted LLD: `docs/LLD/query-agent.md` `topic_selector.py` section (lines 21-30) — no named
   combining/capping function; only the `k + 2k` invariant and its rationale ("prevent context
   blow-up") are specified. Cross-checked `docs/HLD.md#7-system-wide-known-risks` (line 119):
   "Graph traversal context blow-up — bounded by a hard file-count cap of `k + 2k`" — confirms this
   is treated as a system-wide invariant, not a local implementation choice, matching the issue text.
4. Touched files: `agents/query/topic_selector.py` (existing 4.4.1+4.4.2 code, read in full),
   `agents/query/test_topic_selector.py`, `agents/query/test_topic_selector_expansion.py` (read for
   test-style/fixture precedent).
5. Source: `agents/ingestion/shortlist.py` consulted as the repo's one precedent for a "pool size /
   top-k cap" free-function module (per 4.4.1/4.4.2's own docstrings, which cite it explicitly as
   precedent) — confirms the module-level `DEFAULT_*` constant + free-function style, but it has no
   multi-source dedup+cap precedent of its own (its cap is a single pool_size, no merging of two
   sources), so no direct code precedent to reuse for dedup logic; designed fresh here.

## Existing module shape (as of commit 7d2f3dd, unmodified by this subtask until implementation step)
- `DEFAULT_K = 3`, `TopicCandidate(file_id, path, score)` frozen dataclass, `SearchCandidatesFn`
  callable alias, `select_top_k(candidates, *, k=DEFAULT_K) -> list[TopicCandidate]` (4.4.1).
- `DEFAULT_INSUFFICIENCY_RATIO = 0.5`, `DEFAULT_EXPANSION_HOPS = 2`, `GraphNeighbor(file_id,
  edge_type, weight, hop)` frozen dataclass, `GraphNeighborsFn` callable alias,
  `ExpansionResult(topic, neighbors)` frozen dataclass, `is_insufficient_alone(topic, top_score, *,
  ratio=...) -> bool`, `expand_insufficient_topics(selected, graph_neighbors, *, hops=..., ratio=...)
  -> list[ExpansionResult]` (4.4.2).

## What 4.4.3 needs to add
A pure function taking:
- the `select_top_k` output (`Sequence[TopicCandidate]`), and
- the `expand_insufficient_topics` output (`Sequence[ExpansionResult]`, each carrying
  `neighbors: list[GraphNeighbor]`)

...and producing ONE final combined, deduplicated, capped file-id list. No new gRPC/DI seam needed
(pure in-memory combination of already-decoded values — no injected callable required, unlike
4.4.1/4.4.2's RPC-delegating functions).

## Dedup design question (explicit reasoning, since dispatch instructions require it be documented)

Issue wording: "the final **selected-file set** never exceeds k+2k total files" and LLD wording:
"The **combined result** is hard-capped at k + 2k total files". Both use set/count language about
distinct *files*, not about "selection events" or "(topic, neighbor) pairs". A file that is both (a)
one of the top-k selected topics' own file, and (b) also returned as a `GraphNeighbor` of a
*different* insufficient topic's expansion, is still physically one file — including it twice would
inflate the reported/consumed context size without adding any new content, directly undermining the
stated purpose of the cap ("prevent context blow-up"). Therefore: **dedup by `file_id` is required**,
counted once per unique file_id, before the `k + 2k` truncation is applied. This directly follows
from "final selected-file set" (a set, not a multiset) and from the cap's own stated purpose.

**Ordering for dedup ties / truncation priority:** top-k selected topics are the primary,
directly-relevant results and must never be silently dropped in favor of expansion neighbors (which
are secondary, exploratory context). So: iterate top-k selected file_ids first (in `select_top_k`'s
own descending-score order), then expansion neighbors second (grouped by the `ExpansionResult`/topic
order that `expand_insufficient_topics` already returns them in, and within each topic's neighbors,
in the order `GraphNeighborsFn` returned them — not re-sorted, since GraphNeighbors's own ordering,
e.g. by weight/hop, is the engine's concern, not this module's). First-seen `file_id` wins; later
duplicates (whether duplicate selected-topic ids, which should not occur but are handled safely
anyway, or duplicate neighbor ids, or a neighbor id colliding with an already-selected topic id) are
simply skipped, not counted again. Then the deduplicated, order-preserved sequence is truncated to
`k + 2k` entries (using the *caller-supplied* `k`, matching `select_top_k`'s own `k`, not
`len(selected)`, since a caller could in principle pass a `selected` list shorter than `k` if fewer
candidates existed — the cap's formula is defined in terms of the tunable `k` hyperparameter per the
LLD/issue wording, not the incidental size of a particular call's `selected` argument).

## No existing test/name collisions
`agents/query/test_topic_selector_cap.py` does not yet exist (confirmed via `ls agents/query/`
above). No symbol named `combine_and_cap`/`combine`/`cap` exists anywhere in `agents/query/` (grep
confirmed clean).
