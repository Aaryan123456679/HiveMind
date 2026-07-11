---
last_synced_commit: 68c3c5c4e504c2be7502959d78d0a3105b8cfeb5
---

# LLD: `agents/ingestion/`

Status: implemented (issues #17, #18, #19, #43, plus the user-authorized
`task-3.4.4-engine-edge-rpc` scope addition). Normalization (`rawdoc.py`, `dispatch.py`,
`normalize_pdf.py`, `normalize_email.py`, `normalize_ticket.py`), shortlisting
(`shortlist.py`), segmentation (`segment.py`), segment execution/wiring (`wiring.py`), and
`ProposeSplit` (`propose_split.py`) are all real, covered by unit and end-to-end smoke
tests (`test_e2e_smoke.py`). See [HLD.md](../HLD.md) for system context.

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

Runs once per document (or per bounded sub-chunk for long documents). The LLM client is
obtained via `agents/llm/factory.py`'s `create_llm_client()` — a config-driven factory
(`LLM_PROVIDER` env var, values `"ollama"`/`"openrouter"`/`"gemini"`) — rather than a
hardcoded provider, so `segment.py` itself has no direct dependency on any one provider.
**Local Ollama remains the recommended default** for cost reasons (high call volume at
ingestion time), but is now a config choice, not a code-level constraint; see
[llm-provider.md](llm-provider.md) for the provider abstraction.

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

`agents/ingestion/wiring.py`'s `execute_segment()` is the real orchestrator that drives
this (a Python-side caller of the RPCs below, not implicit Go-side behavior):

- Resolves `APPEND_EXISTING`'s `target_topic` to a real fileID via a caller-supplied
  resolver, failing fast (`TopicNotFoundError`) *before* any RPC if it can't be resolved.
- Executes the append/create via [`engine/rpc/`](rpc.md)'s `PutSegment` — now including the
  segment's real topic path (`PutSegmentRequest.path`, closing issue #43), so newly created
  files are discoverable via `SearchCandidates` immediately, not just addressable by fileID.
- `entities` feed a dedicated `entity.idx` B+Tree (via the additive `LookupEntity`/
  `PutEntity` RPCs) and increment `ENTITY_COOCCUR` edge weights in [`engine/graph/`](graph.md)
  via `PutEdge`, tracking cross-file entity co-occurrence.
- `related_topics` become `LLM_ASSERTED` edges in the same graph, also via `PutEdge`.
- Error handling is fail-fast through the `PutSegment` call (nothing yet committed) and
  best-effort-with-error-collection afterward (each entity/edge operation attempted
  independently; failures collected in `SegmentExecutionResult.errors` rather than rolling
  back the already-durable content write).

`PutEdge`, `PutEntity`, and `LookupEntity` are additive RPCs beyond the original six-RPC
proto surface, added by the user-authorized `task-3.4.4-engine-edge-rpc` scope to give
`entity.idx`/edge-write operations a real backing RPC (see `.cdr/commits/task-3.4.4.md`).

## `ProposeSplit`

```
ProposeSplit(fileContent) -> [{newPath, sectionRanges}, ...] + redirect summary
```

Called by [`engine/split/`](split.md) when a file crosses its auto-split size threshold. Produces
a topic-coherent split plan; the Go engine executes the plan atomically (allocation, writes,
catalog updates, graph edges — see [split.md](split.md) for the full sequence).

## Interactions with other modules

- `engine/rpc/` — `PutSegment`, `PutEdge`, `PutEntity`, `LookupEntity` execution targets;
  `ProposeSplit` is exposed to the engine as a callee.
- `engine/graph/` — entity co-occurrence and LLM-asserted edges (written via `PutEdge`).
- `agents/llm/` — provider abstraction (`create_llm_client()` factory) for the segmentation
  and split-proposal LLM calls.
- `engine/btree/` — source of the shortlisted candidate topic list, via `shortlist.py`'s
  `GrpcSearchCandidatesClient` wrapping the real `SearchCandidates` RPC.

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
