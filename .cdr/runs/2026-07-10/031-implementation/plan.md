# Plan

1. `agents/ingestion/propose_split.py`:
   - `ProposeSplitError` (base) / `ProposeSplitParseError` (malformed LLM output or
     unresolvable marker), mirroring `segment.py`'s exception design.
   - `SectionRange` frozen dataclass `(start: int, end: int)`.
   - `SplitFileProposal` frozen dataclass `(new_path: str, section_ranges: list[SectionRange])`.
   - `ProposeSplitResult` frozen dataclass `(files: list[SplitFileProposal], redirect_summary: str)`.
   - `_SPLIT_PROMPT_TEMPLATE`: asks the LLM for ordered JSON `{"sections": [{"new_topic_path", "start_marker"}, ...], "redirect_summary": str}`, >= 2 sections, no markdown code fences.
   - `_strip_code_fences(raw: str) -> str`: defensive fence-stripping before `json.loads`, proactively avoiding segment.py's open F1 finding rather than reproducing it.
   - `_parse_propose_split_json(raw: str) -> tuple[list[dict], str]`: structural validation (types, required fields, >=2 sections, non-empty/unique new_topic_path).
   - `_resolve_section_ranges(content: str, sections: list[dict]) -> list[SectionRange per section]`: deterministic marker-search + partition construction (see architecture-discovery.md).
   - `propose_split(file_content: bytes, llm_client: LLMClient, *, model=None, temperature=0.0, max_tokens=None, timeout=None) -> ProposeSplitResult`: decode utf-8, build prompt, call `llm_client.complete()`, parse, resolve ranges, build one `SplitFileProposal` per section (one contiguous `SectionRange` each).
2. `agents/ingestion/test_propose_split.py`: mocked `LLMClient` fixture (over-threshold multi-section document), assert:
   - happy path: ranges partition content (sorted, contiguous, `ranges[0].start == 0`, `ranges[-1].end == len(content)`, no gaps/overlaps across the whole plan).
   - each proposal's own single range is well-formed (start < end).
   - redirect_summary passed through.
   - malformed-response cases (non-JSON, missing field, <2 sections, unresolvable marker, out-of-order marker, duplicate new_topic_path) each raise `ProposeSplitParseError` with a descriptive message.
   - markdown-code-fence-wrapped valid JSON is still parsed successfully (regression guard vs. segment.py's F1 class of bug).
   - LLMError from the client propagates unwrapped (mirrors segment.py's documented convention).
3. Run `go build ./...`, `go vet ./...`, `go test ./... -race` (x2) in `engine/` -- expect no change since nothing Go-side is touched; this validates no accidental Go-side impact.
4. Run `agents/` pytest suite (full, plus targeted new file) to confirm suite-wide green.
5. Write validation-matrix.json mapping test spec's exact acceptance line to test cases.
6. self-consistency.json (build/vet/test green, matrix covered) -- internal only, NOT verification.
7. One local commit, Problem/Solution/Impact format.
8. handoff.json with pointers only.
