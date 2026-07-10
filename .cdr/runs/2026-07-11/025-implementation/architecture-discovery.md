# Architecture discovery — 4.5.1

## LLD

`docs/LLD/query-agent.md` `synthesizer.py` section (verbatim): "Final LLM call: refined
intent + concatenated selected markdown (with file-path headers) -> answer with inline
file-path citations." No wire-format detail beyond this one sentence; no header format
specified (disclosed choice below). Pipeline order confirms `synthesizer.py` is the last
stage, consuming `topic_selector.py`'s output (which the LLD/topic_selector.py module
docstring says currently stops at a deduplicated `list[int]` of `file_id`s — mapping
file_id -> path/content for the synthesizer prompt is explicitly left to a later,
not-yet-built subtask per `topic_selector.py`'s own "4.4.3" comment block). This subtask's
own acceptance criteria/test spec take the concatenated markdown as an already-assembled
input string, so `synthesize_answer()` does not need to consume `topic_selector.py`'s
`list[int]` output directly — consistent with 4.5.1's "impacted modules" listing only
`synthesizer.py`/`test_synthesizer.py` (no topic_selector wiring change).

## Existing conventions (read directly, not assumed)

- `agents/query/intent_refiner.py`: DI `LLMClient` under `TYPE_CHECKING`; frozen dataclass
  result (`IntentRefinerResult`); prompt-template constant + `_build_prompt`; call
  `llm_client.complete(prompt, model=, temperature=, max_tokens=, timeout=)`; strip code
  fences via `ingestion._json_fences.strip_code_fences`; `json.loads` + manual field/type
  validation; two exception classes `FooError` (base, NOT a subclass of `llm.client.LLMError`)
  and `FooParseError` (all malformed-output cases, descriptive message per failure).
- `agents/query/topic_selector.py`: plain free functions (no class/service object), DI via
  `Callable` type aliases for not-yet-wired RPCs, frozen dataclasses for domain records,
  module-level `DEFAULT_*` constants (not inline literals) for tunables.
- `agents/llm/client.py`: `LLMClient.complete(prompt, *, model=None, temperature=0.0,
  max_tokens=None, timeout=None) -> str`; raises `LLMError` on provider failure (never
  converted to this module's own parse-error type — propagated unwrapped, per
  `intent_refiner.refine_intent`'s own precedent).
- `agents/ingestion/_json_fences.py`: `strip_code_fences(raw: str) -> str`, shared helper,
  already imported cross-package by `intent_refiner.py` (`from ingestion._json_fences import
  strip_code_fences`) — same import path reused here.
- `agents/query/test_intent_refiner.py`: `_FakeLLMClient(LLMClient)` subclass (captures
  calls, returns canned response or raises canned error) — same fixture shape to mirror in
  `test_synthesizer.py`.
- Package layout: `agents/pyproject.toml` declares `packages = ["ingestion", "query", "llm",
  "eval"]`, `testpaths = ["."]`; tests run from `agents/` as cwd so `from query.synthesizer
  import ...` / `from llm.client import ...` / `from ingestion._json_fences import ...`
  resolve as top-level installed packages (`pip install -e .` dev install), matching
  `test_intent_refiner.py`'s own import style.

## Disclosed choices for this module (LLD silent on these)

- **File-path header format**: LLD does not specify the exact header syntax. This module
  documents and expects `## File: <path>` (level-2 markdown heading, literal `File:` label)
  as the convention `selected_markdown` sections use — chosen because it is unambiguous to
  regex-extract, human-readable, and matches ordinary markdown heading syntax (no invented
  custom delimiter). `synthesize_answer()` does not require every line to match — it simply
  extracts whichever such headers are present to know the "actually provided" path set; if a
  future upstream caller uses this format, headers round-trip correctly.
- **LLM response wire format**: per this run's `requirement.md`, JSON object with `answer`
  (str, prose containing inline `[<path>]` citations) and `citations` (list[str], flat
  dedup'd list of cited paths) — mirrors `intent_refiner.py`'s JSON-object convention rather
  than inventing a new raw-markdown-with-regex-scraped-citations scheme.
- **Citation validation is NOT this subtask's job**: `SynthesizerResult` exposes an
  `unknown_citations()` method (citations not present in the parsed `provided_paths`) as a
  building block, but no exception/rejection behavior is implemented here — that is 4.5.2.
