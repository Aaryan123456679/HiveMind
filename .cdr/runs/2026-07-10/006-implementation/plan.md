# Plan — task-3.4.3

## `agents/ingestion/segment.py`

- `SegmentAction = Literal["APPEND_EXISTING", "CREATE_NEW"]`.
- `SegmentError(Exception)` — base for this module's own errors (parallel to
  `LLMError` for `agents/llm/`; kept distinct from `LLMError` since `LLMError`
  is specifically "provider call failed", whereas this module's errors are
  "provider call succeeded but returned unusable output").
- `SegmentParseError(SegmentError)` — raised for every malformed-output case:
  unparseable JSON, missing required field, wrong field type, internally
  inconsistent field combination. One exception type, with a specific,
  descriptive message per failure reason (not a generic `Exception`, and not
  a proliferation of near-duplicate exception subclasses the issue doesn't
  ask for).
- `@dataclass(frozen=True) SegmentResult`: `topic_action: SegmentAction`,
  `target_topic: str`, `new_topic_path: str`, `content_markdown: str`,
  `entities: list[str]`, `related_topics: list[str]`.
  - `target_topic`/`new_topic_path` are always both present as strings
    (empty string `""` for the one that doesn't apply to the given
    `topic_action`), matching the LLD's flat JSON shape and avoiding
    `Optional`-typed fields downstream consumers (3.4.4 wiring) would need to
    null-check.
- `_SEGMENT_PROMPT_TEMPLATE` module constant + `_build_prompt(doc, shortlist)`
  helper: embeds `doc.text` and the shortlist's topic paths, instructs the
  model to return ONLY a JSON object with exactly the required keys, gives
  the enum values for `topic_action`, and gives a one-line description of
  each field.
- `_parse_segment_json(raw: str) -> SegmentResult`:
  1. `json.loads(raw)` -> `SegmentParseError` on `json.JSONDecodeError`
     (message includes the underlying decode error).
  2. top-level must be a `dict` -> `SegmentParseError` if not.
  3. Required-field presence check for all 6 keys -> `SegmentParseError`
     naming the missing key(s).
  4. Type check per field (`topic_action`/`target_topic`/`new_topic_path`/
     `content_markdown` must be `str`; `entities`/`related_topics` must be
     `list` of `str`) -> `SegmentParseError` naming the offending field and
     expected vs actual type.
  5. `topic_action` must be one of the two literal values -> `SegmentParseError`.
  6. Cross-field consistency:
     - `APPEND_EXISTING` => `target_topic` must be a non-empty string.
       Strictness decision (disclosed): checked against the shortlist's
       known paths defensively (log-free, just validated) is arguably too
       strict — an LLM choosing an existing topic not currently in the
       *shortlist* could still be a legitimate existing topic elsewhere in
       the catalog the shortlist happened not to surface (shortlist is a
       bounded pre-filter, not an exhaustive membership list per
       `shortlist.py`'s own docstring). So: validate non-empty-string only,
       do NOT hard-require membership in `shortlist`'s paths. This keeps
       validation about *structural* correctness of the LLM's JSON, not a
       second-guess of its topic-selection judgment.
     - `CREATE_NEW` => `new_topic_path` must be a non-empty string.
     - Whichever field doesn't apply to the given `topic_action` is not
       required to be empty (again: not re-litigating LLM judgment), but if
       both `target_topic` and `new_topic_path` are empty for a given
       `topic_action`'s required field, that's the failure case above.
  7. Construct and return `SegmentResult`.
- `segment(doc: RawDocument, shortlist: Sequence[TopicCandidate], llm_client:
  LLMClient, *, model=None, temperature=0.0, max_tokens=None, timeout=None) ->
  SegmentResult`: builds the prompt, calls
  `llm_client.complete(prompt, model=model, temperature=temperature,
  max_tokens=max_tokens, timeout=timeout)`, parses the result via
  `_parse_segment_json`. `LLMError` from `.complete()` itself propagates
  un-wrapped (caller already knows how to handle that from 3.4.1); only
  output-shape problems become `SegmentParseError`.

## `agents/ingestion/test_segment.py`

- Fake `LLMClient` subclass (`_FakeLLMClient`) whose `.complete()` returns a
  pre-configured canned string, capturing the prompt it was called with for
  assertions (mirrors "LLMClient mocked" test-spec wording; a subclass is
  used rather than `MagicMock(spec=LLMClient)` for cleaner type-correctness
  with the ABC, consistent with `agents/llm/test_ollama_client.py`'s own
  style — will check that file too before finalizing).
- Fixture `RawDocument` + fixture `list[TopicCandidate]` shortlist.
- Well-formed cases: valid JSON for `APPEND_EXISTING`, valid JSON for
  `CREATE_NEW` -> assert correct `SegmentResult` fields.
- Malformed cases, each asserting `pytest.raises(SegmentParseError, match=...)`:
  - unparseable JSON string
  - valid JSON, missing a required field (parametrized over each of the 6 keys)
  - valid JSON, wrong type for a field (e.g. `entities` as a string, not a list)
  - valid JSON, `topic_action` not one of the two literals
  - valid JSON, `APPEND_EXISTING` with empty `target_topic`
  - valid JSON, `CREATE_NEW` with empty `new_topic_path`
- Assert prompt sent to `.complete()` contains the document text and the
  shortlist's topic paths (prompt-construction sanity, not over-specified).

## Self-consistency checks before commit

- `pytest agents/ingestion/test_segment.py -v`
- `pytest agents/ -q` (full regression)
- `ruff check agents/ingestion/segment.py agents/ingestion/test_segment.py`
