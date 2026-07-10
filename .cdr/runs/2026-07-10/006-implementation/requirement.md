# Requirement — issue #18 subtask 3.4.3

Segmentation agent epic (milestone #5 "Phase 3"), subtask 3/6.

## Acceptance criteria (from `gh issue view 18`, cross-checked against
`docs/LLD/ingestion-agent.md` "Segmentation agent" section)

Given document text + a topic shortlist, a segmentation call returns a string
(via `LLMClient.complete()`) that is parsed into a structured segment result
with fields:

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

Malformed LLM output (unparseable JSON, missing required field, wrong type,
internally-inconsistent field combination) must be rejected with a clear,
specific error — never a silently degraded/partial result.

## Test spec

`pytest agents/ingestion/test_segment.py` — `LLMClient.complete()` mocked;
covers well-formed JSON for both `topic_action` values, and malformed-response
cases (unparseable JSON, missing field, wrong type, inconsistent combination).

## Impacted modules

`agents/ingestion/segment.py`, `agents/ingestion/test_segment.py` (both new).

## Upstream contracts this subtask must line up with

- `agents/llm/client.py` (3.4.1, verified): `LLMClient.complete(prompt, *,
  model=None, temperature=0.0, max_tokens=None, timeout=None) -> str`, raises
  `LLMError` subclasses on provider failure. `segment()` takes an `LLMClient`
  instance as a dependency.
- `agents/ingestion/shortlist.py` (3.4.2, verified): `shortlist()` returns
  `list[TopicCandidate]` (`file_id: int`, `path: str`, `score: float`). This
  is the "topic shortlist" input shape.
- `agents/ingestion/rawdoc.py` (3.3.4, verified): `RawDocument` (`id`,
  `source_type`, `text`, `structured_fields`, `timestamp`) is the pipeline's
  document shape; `.text` is "the primary content downstream segmentation
  operates on" per its own docstring.

## LLD cross-check

`docs/LLD/ingestion-agent.md` confirms `topic_action` is exactly
`APPEND_EXISTING | CREATE_NEW` (matches the issue body's own truncated
fragment), and that `entities`/`related_topics` are plain lists (LLD shows
`entities: []`, `related_topics: []` with no richer element shape specified).
`proto/hivemind.proto`'s `PutSegmentRequest` (the RPC this segment eventually
feeds) only carries `file_id` + raw `content` bytes — no schema for
entities/related_topics is defined at the RPC layer either, so there is no
externally-imposed richer shape to match. Decision: `entities: list[str]`,
`related_topics: list[str]` (plain topic-path strings), consistent with LLD's
literal `[]` and with `shortlist.py`'s own plain-string `path` convention.
