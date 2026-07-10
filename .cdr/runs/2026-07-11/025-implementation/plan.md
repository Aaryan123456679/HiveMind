# Plan — 4.5.1

1. `agents/query/synthesizer.py`:
   - Module docstring disclosing: JSON wire-format choice, `## File: <path>` header
     format choice, decoupled-scalar params (not `IntentRefinerResult`), deferral of
     citation rejection to 4.5.2.
   - `_FILE_HEADER_RE` regex for `## File: <path>` headers; `_extract_provided_paths()`
     helper — dedup, order-preserved.
   - `_SYNTHESIS_PROMPT_TEMPLATE` embedding refined_intent, query_type, entities, and the
     verbatim `selected_markdown` block (headers preserved as-is in the prompt, satisfying
     "prompt includes file-path headers").
   - `SynthesizerError` (base, not `LLMError` subclass) / `SynthesizerParseError`.
   - `SynthesizerResult` frozen dataclass: `answer: str`, `citations: list[str]`,
     `provided_paths: list[str]`; method `unknown_citations() -> list[str]` (citations not
     in provided_paths, dedup, order-preserved).
   - `_parse_synthesis_json(raw, provided_paths) -> SynthesizerResult`: strip_code_fences +
     json.loads + validate `answer` (str) and `citations` (list[str]) fields.
   - `synthesize_answer(refined_intent, query_type, entities, selected_markdown,
     llm_client, *, model=None, temperature=0.0, max_tokens=None, timeout=None) ->
     SynthesizerResult`: build prompt, call `llm_client.complete()`, parse.
2. `agents/query/test_synthesizer.py`:
   - `_FakeLLMClient(LLMClient)` fixture (mirrors `test_intent_refiner.py`).
   - Fixture citation-containing LLM JSON response with 2 file headers in the input
     markdown, citations list referencing both real paths.
   - Assert prompt (captured via fake client's `.calls`) contains both `## File: <path>`
     header lines verbatim.
   - Assert parsed result's `answer` and `citations` match fixture.
   - Assert `unknown_citations()` is empty when all citations are in provided set, and
     non-empty when a fixture citation references a path not present in the input markdown
     (building-block coverage for 4.5.2, without implementing 4.5.2's own dedicated test
     file).
   - Malformed-response cases mirroring `test_intent_refiner.py`'s precedent: unparseable
     JSON, missing field, wrong type -> `SynthesizerParseError`; code-fence tolerance;
     `LLMError` propagation unwrapped.
3. Run `cd agents && python3 -m pytest query/ -q`, then full regression
   `python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q` (expect only the 2
   pre-existing protobuf failures from issue #46), then `ruff check agents/query/`.
4. Self-consistency write-up, ONE local commit (Problem/Solution/Impact style, no push),
   handoff.json (pointers only).
