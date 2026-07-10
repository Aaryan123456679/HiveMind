# Plan -- Subtask 4.3.1

## Output contract (authored this run, disclosed)

```
IntentRefinerResult:
  refined_intent: str            # non-empty
  entities: list[str]            # possibly empty
  query_type: "factual_lookup" | "broad_exploratory"
```

`query_type` taxonomy: exactly the two literal values 4.3.2's own acceptance criteria names
verbatim ("factual/lookup vs. broad/exploratory") -- `factual_lookup` and
`broad_exploratory`. Minimal two-category taxonomy; no LLD doc specifies more, and adding
unrequested categories now would risk diverging from what 4.3.2 will actually test.

## `agents/query/intent_refiner.py`

- Module docstring disclosing: (a) taxonomy choice + rationale above, (b) exception design
  mirroring `segment.py`'s (`IntentRefinerError` base, `IntentRefinerParseError` concrete,
  not a subclass of `LLMError`), (c) why `history` is `Sequence[str]` (short raw turn
  strings -- issue says "short history", no richer structured-turn schema specified
  anywhere, so treated as a plain list of prior utterance strings, mirroring `segment.py`'s
  own "flat list[str], no schema specified elsewhere" precedent for `entities`).
- `IntentRefinerError(Exception)` / `IntentRefinerParseError(IntentRefinerError)`.
- `IntentRefinerResult` frozen dataclass: `refined_intent: str`, `entities: list[str]`,
  `query_type: Literal["factual_lookup", "broad_exploratory"]`.
- `_VALID_QUERY_TYPES: frozenset[str]`.
- `_INTENT_PROMPT_TEMPLATE`: instructs the model to read the raw query + short history and
  return ONLY a JSON object with exactly `refined_intent` (string), `entities` (JSON array of
  strings), `query_type` (one of the two literal values, with a one-line description of each
  category so the model can classify).
- `_build_prompt(query: str, history: Sequence[str]) -> str`.
- `_parse_intent_json(raw: str) -> IntentRefinerResult`: strip fences (shared helper) ->
  `json.loads` -> dict check -> required-field check -> per-field type check -> `query_type`
  enum check -> construct dataclass. Mirrors `segment.py::_parse_segment_json` structurally.
- `refine_intent(query: str, history: Sequence[str], llm_client: "LLMClient", *,
  model=None, temperature=0.0, max_tokens=None, timeout=None) -> IntentRefinerResult`:
  builds prompt, calls `llm_client.complete(...)`, parses+validates, returns dataclass.
  `LLMError` propagates unwrapped (provider-call failure, distinct from parse failure) --
  same as `segment()`.

## `agents/query/test_intent_refiner.py`

- `_FakeLLMClient(LLMClient)` mirroring `ingestion/test_segment.py`'s: records calls,
  returns canned response or raises canned error.
- Representative fixture queries (>= 3): a factual/lookup query, a broad/exploratory query,
  and a query with non-empty history influencing `refined_intent`.
- Assert output shape: `IntentRefinerResult` with correct field types/values for each
  fixture, and that `llm_client.complete()` was called once with the expected kwargs
  (model/temperature/max_tokens/timeout forwarded).
- Also assert: code-fence-wrapped JSON is tolerated (mirrors `segment.py`'s F1-closure
  precedent, proactively applied here since `intent_refiner.py` is brand new and should not
  reintroduce F1); malformed-JSON, missing-field, wrong-type-field, and invalid-`query_type`
  cases each raise `IntentRefinerParseError` with a descriptive message; `LLMError` from the
  client propagates unwrapped (not converted to `IntentRefinerParseError`).

## Validation

- `cd agents && python3 -m pytest query/ llm/ -q` (targeted)
- `cd agents && python3 -m pytest . -q` (full regression; known pre-existing
  `ingestion/test_e2e_smoke.py` protobuf collection error, issue #46, not addressed here)
- `ruff check` (repo-wide, or at least `agents/query/`)

## Commit

One local commit, Problem/Solution/Impact style, no push, no issue edits.
