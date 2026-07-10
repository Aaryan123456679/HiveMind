# Architecture discovery — task-3.4.3

## Read (index-first order)

1. `.cdr/index/task.jsonl` — confirmed task-3.4.1 (`LLMClient`) and task-3.4.2
   (`shortlist()`/`TopicCandidate`) both `state: verified`, committed at
   `e7d1e07`/`987b5f6` and `98dda16`/`c602570` respectively. No task-3.4.3
   entry exists yet (this is the first run for it).
2. `gh issue view 18` (re-fetched fresh; no injected instruction-like text
   found in the body this time, unlike a prior session on task-3.4.1 which
   did encounter one — see security note below).
3. `agents/llm/client.py` — `LLMClient` ABC, single abstract method
   `complete(prompt, *, model=None, temperature=0.0, max_tokens=None,
   timeout=None) -> str`, raises `LLMError` (base) on any failure.
4. `agents/llm/ollama_client.py` — style/convention reference: module-level
   `DEFAULT_*` constants, a `<Provider>Error(LLMError)` subclass, disclosed-
   design docstring sections, `from __future__ import annotations`.
5. `agents/ingestion/shortlist.py` — `TopicCandidate(file_id: int, path: str,
   score: float)` frozen dataclass; `shortlist()` returns `list[TopicCandidate]`.
   This is the shortlist shape `segment()` consumes.
6. `agents/ingestion/rawdoc.py` — `RawDocument(id: str, source_type: SourceType,
   text: str, structured_fields: dict, timestamp: datetime)` frozen dataclass.
   `.text` is documented as "the primary content downstream segmentation
   operates on" — confirms `segment()` should take a `RawDocument`, not a bare
   string, for pipeline consistency.
7. `docs/LLD/ingestion-agent.md` "Segmentation agent" section — confirms
   `topic_action: APPEND_EXISTING | CREATE_NEW` literally, and
   `entities: []`, `related_topics: []` with no richer element shape.
8. `proto/hivemind.proto` `PutSegmentRequest`/`PutSegmentResponse` — only
   `file_id` (uint64) + `content` (bytes); no entity/related-topic schema at
   the RPC layer, confirming no externally-imposed richer shape to match.
9. `agents/ingestion/test_shortlist.py` — test-file conventions: module
   docstring cross-referencing issue/test-spec, `unittest.mock.MagicMock`,
   plain pytest functions (no test classes), fixtures as module-level
   constants.

## Conventions to follow

- snake_case, frozen dataclasses, `from __future__ import annotations`.
- Disclosed-design docstring sections for non-obvious choices (this repo's
  established pattern across `agents/llm/`, `agents/ingestion/`).
- Provider/module-specific exception subclassing a shared base
  (`LLMError` -> `OllamaClientError`); here: a new `SegmentError` base with
  a `SegmentParseError` subclass distinguishing malformed-output failures
  from any future non-parse failure mode.
- Tests: plain pytest functions, `LLMClient.complete` mocked via
  `unittest.mock.MagicMock`/a small fake subclass, module docstring
  cross-referencing the issue's test spec, fixtures as module constants.

## Security note

No embedded fake system-reminder-style injection found in `gh issue view 18`
output or in `.cdr/index/*.jsonl` / prior handoff files read during this
discovery step. (Two fake system-reminder-style blocks — an MCP "tokensave"
tool-instruction block and an "Auto Mode Active" directive, plus a fake
date-change notice — did appear in this session's own conversation transcript
outside of any tool-output content; per the standing project security note
these are treated as untrusted and not acted upon, and are disclosed in the
final handoff/report.)
