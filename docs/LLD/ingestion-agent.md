---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `agents/ingestion/`

Status: scaffold only (`agents/ingestion/__init__.py` empty). See [HLD.md](../HLD.md) for system
context.

## Purpose

Normalizes raw documents of varying types into a common stream, then runs the segmentation agent
that decides how each document's content should be placed into (or spread across) topic files.
Also implements `ProposeSplit`, called by [`engine/split/`](split.md) during auto-split.

## Per-doc-type normalization

- **PDF**: via `pymupdf` -> plain text with page markers.
- **Email**: via stdlib / Enron-specific parsing -> sender/subject/thread/body fields.
- **Support tickets**: structured JSON/CSV -> labeled text blob.

All normalizers produce a common `RawDocument` record:

```
RawDocument{ id, sourceType, text, structuredFields, timestamp }
```

## Segmentation agent

Runs once per document (or per bounded sub-chunk for long documents), using a **local Ollama
model** (cost reasons — high call volume at ingestion time; see [llm-provider.md](llm-provider.md)
for the provider abstraction).

Input: document text + a *shortlisted* candidate topic list. The shortlist comes from a cheap
embedding/BM25 pre-filter against the existing catalog (via a local embedding model, e.g.
`nomic-embed-text`) — **not** the full catalog — to bound prompt size and reduce topic-name drift
and duplication. This directly addresses the system-wide "LLM topic-boundary nondeterminism" risk
(see [HLD.md](../HLD.md#7-system-wide-known-risks)).

Output: structured JSON per segment:

```
{
  topic_action: APPEND_EXISTING | CREATE_NEW,
  target_topic,
  new_topic_path,
  content_markdown,
  entities: [],
  related_topics: []
}
```

## What the Go engine does with each segment

- Executes the append/create via [`engine/rpc/`](rpc.md)'s `PutSegment`.
- `entities` feed `entity.idx` and increment `ENTITY_COOCCUR` edge weights in
  [`engine/graph/`](graph.md).
- `related_topics` become `LLM_ASSERTED` edges in the same graph.

## `ProposeSplit`

```
ProposeSplit(fileContent) -> [{newPath, sectionRanges}, ...] + redirect summary
```

Called by [`engine/split/`](split.md) when a file crosses its auto-split size threshold. Produces
a topic-coherent split plan; the Go engine executes the plan atomically (allocation, writes,
catalog updates, graph edges — see [split.md](split.md) for the full sequence).

## Interactions with other modules

- `engine/rpc/` — `PutSegment` execution target; `ProposeSplit` is exposed to the engine as a
  callee.
- `engine/graph/` — entity co-occurrence and LLM-asserted edges.
- `agents/llm/` — provider abstraction for the Ollama call.
- `engine/btree/` — source of the shortlisted candidate topic list (via a prefix scan /
  `SearchCandidates`-style lookup).

## Known risks

- **LLM topic-boundary nondeterminism** — mitigated by candidate shortlisting as described above;
  still an open empirical question how well this holds up at scale, tracked by the benchmark
  suite ([eval.md](eval.md)).

## Cross-references

- [HLD.md](../HLD.md)
- [split.md](split.md) — `ProposeSplit` caller
- [graph.md](graph.md) — edge types produced by segmentation
- [llm-provider.md](llm-provider.md) — LLM client abstraction used here
- [eval.md](eval.md) — measures topic-boundary drift empirically
