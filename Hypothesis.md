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
topics that actually hold the missing relevant content, so widening k alone can't reach them. I
did not have time to isolate which of those two it is (that would mean instrumenting
`select_top_k`'s actual candidate scores and `is_insufficient_alone`'s decisions per-query, not
just the aggregate recall number) -- that's the natural next experiment, not this one.
