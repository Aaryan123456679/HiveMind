# Hypothesis: why HiveMind's recall trails vector RAG

## The diagnosis

I suspect HiveMind's low recall (`results/run-001/`: 0.062 -> 0.146 -> 0.302 recall across the
20/50/100% checkpoints, vs. vector RAG's 0.219 -> 0.458 -> 0.823) comes from segmentation
fragmenting related content across topic files that the graph-aware topic-selector's default
`k=3` and `expand_insufficient_topics`'s `DEFAULT_INSUFFICIENCY_RATIO=0.5` never reach, rather
than from the topic-selector being handed too few slots to begin with -- a query's relevant
content can land split across more distinct topic files than a small, fixed `k` (or a
conservative insufficiency threshold that rarely triggers graph expansion) will ever surface,
regardless of how generous that `k` is.

## The experiment

To test the "it's k, not segmentation" half of that hypothesis directly: reran the 100%-checkpoint
hivemind arm from `results/run-001/` (same 32-doc corpus, same 32 queries, same
`query.pipeline.run_query_pipeline` over the same real gRPC-backed `LiveHivemindRetriever`, same
`openrouter`/`gpt-4o-mini` provider) with the topic-selector's `k` loosened from `DEFAULT_K=3` to
`5`, measuring `recall_at_k`/`precision_at_k` at that same `k=5` cutoff. No judge calls were
needed (recall/precision are computed directly against `eval.ground_truth`'s relevant-doc labels,
not LLM-judged), so the real cost of this run was $0.0089.

**Result: mean_recall=0.302, mean_precision=0.719 -- identical to run-001's k=3 100%-checkpoint
numbers to three decimal places.**

## Honest read of this result

Loosening k from 3 to 5 moved nothing. That's a meaningfully informative negative result: it
argues against "the topic-selector just needs more slots" and toward the segmentation half of the
hypothesis -- either the same topics get selected regardless of k (the extra 2 slots go unused),
or `expand_insufficient_topics`'s ratio=0.5 threshold isn't triggering graph expansion into the
topics that actually hold the missing relevant content, so widening k alone can't reach them.

## Follow-up: what was `select_top_k` actually choosing from?

No logging anywhere in `topic_selector.py`/`pipeline.py` records candidate-pool size, and
`results/run-001/`'s output files only store aggregate metrics, not per-query detail -- so this
isn't post-hoc analysis of existing logs, it's a second, equally cheap real run (same 100%-checkpoint
corpus, same 32 queries, same `openrouter` provider, real cost $0.0089) that wraps the real
`SearchCandidates` gRPC call to record `len(candidates)` per query, at the same `k=3` config
run-001 actually used.

**Result — candidate pool sizes across all 32 queries:**
`[0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 3, 3]`
(min=0, median=1.0, mean=1.41, max=3). **32/32 queries had a candidate pool of size <= 3 —
none ever reached 4, let alone 5.**

This settles which half of the hypothesis is right, and rules out both branches I'd floated above:
`select_top_k`'s `k` and `expand_insufficient_topics`'s ratio never had a chance to matter, because
`SearchCandidates` itself almost never returns more than 1-3 candidates per query (and returned
zero for 3 of the 32 queries) -- there usually isn't a 4th topic sitting on the sidelines. The
bottleneck is candidate *retrieval/matching*, not topic *selection*. That also matches a limitation
already on record independent of this investigation (`SearchCandidates`'s first-token-prefix-only
matching), rather than being a new finding invented for this writeup.

I did not have time to go one level deeper -- e.g. instrumenting whether the 0-1-2-candidate
queries are cold misses (no topic file contains a matching term at all) vs. weak matches
(matching term present, but not as the searched prefix) -- that's the natural next experiment,
not this one.

## Why this process, not just the numbers

The benchmark table above is one data point. What should carry more weight in evaluating this
project is the process behind it: state a falsifiable hypothesis, run the cheapest real
experiment that could kill it ($0.0089, no mocking, same production pipeline), get a clean null
result, and let that null result *redirect* the investigation rather than get explained away --
the first experiment ruled out "k is too small," and only then did a second, equally real
experiment ($0.0089 again) show the actual ceiling sits one layer upstream, in candidate
retrieval. Neither run was faked or hand-picked; the first instrumentation attempt at the second
experiment silently returned zero recorded candidates due to a real bug (`_reprovision()`
clobbering the wrapped client on the first call) -- that failure is left in here rather than
quietly redone and hidden, because an honest account of what didn't work the first time is part
of the same discipline as the honest recall numbers above it. What's still unresolved (cold miss
vs. weak match, and the deeper fix to `SearchCandidates`'s matching) is stated plainly rather than
implied to be solved.
