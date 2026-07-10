# Plan -- 4.5.2

1. Create `agents/query/test_synthesizer_citations.py`:
   - Reuse the same `_FakeLLMClient` pattern as `test_synthesizer.py` (a minimal `LLMClient`
     ABC subclass capturing calls and returning a canned response) -- duplicated locally per
     existing package convention (flagged non-blocking in `F-4.4.4-2`; not a new duplication
     class since `test_synthesizer.py` already does this for the same module).
   - Define a fixture `selected_markdown` with exactly two "## File: <path>" headers
     (the "selected-file input set" from the acceptance criteria).
   - Define a fixture LLM JSON response whose `answer` prose contains inline citation
     markers for (a) one path that matches a real header (valid) and (b) one path that does
     not appear in any header (hallucinated), and whose `citations` field lists both.
   - Call `synthesize_answer(...)` through the fake client (end-to-end, per the "detected in
     the synthesized answer" wording of the acceptance criteria -- not calling
     `unknown_citations()` in isolation on a hand-built `SynthesizerResult`).
   - Assert:
     - The valid citation is NOT in `result.unknown_citations()`.
     - The hallucinated citation IS in `result.unknown_citations()` (and is the only entry).
     - `result.citations` still contains both (i.e. detection doesn't silently drop/mutate
       the reported citation list -- flagging is exposed to the caller as a distinct signal,
       not baked into a filtered `citations`).
   - Add a second test with roles reversed (hallucinated first, valid second) to confirm
     order/position of the hallucinated citation within `answer`/`citations` doesn't affect
     detection -- guards against an accidental positional assumption in
     `unknown_citations()` or in this new test's own fixture wiring.
   - Add a docstring at module top explaining the "no synthesizer.py change" decision inline
     (pointing back to this run's requirement.md) so a future reader of just this test file
     understands why it only imports, not modifies, synthesizer.py.
2. No synthesizer.py change (see requirement.md decision).
3. Run `cd agents && python3 -m pytest query/ -q`.
4. Run full regression: `cd agents && python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q`,
   confirm only the 2 pre-existing protobuf failures (issue #46) remain.
5. Run `ruff check agents/query/`.
6. Write self-consistency.json.
7. One local commit (Problem/Solution/Impact style), no push.
8. Write handoff.json (pointers only).
