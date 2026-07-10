# Architecture Discovery -- 4.3.2

## Token order followed
index/ (none relevant to this greenfield test file) -> memory/handoffs
(`.cdr/runs/2026-07-11/011-implementation/handoff.json`, `.cdr/index/regression.jsonl`
F-4.3.1-1/2/3) -> `gh issue view 22` -> `agents/query/intent_refiner.py` ->
`agents/query/test_intent_refiner.py` -> `agents/pyproject.toml` (test config).

## Existing contract (`agents/query/intent_refiner.py`, unmodified, read-only)
- `QueryType = Literal["factual_lookup", "broad_exploratory"]` -- closed 2-value
  taxonomy (per F-4.3.1-3, extending it later requires touching this module, not
  just fixtures -- out of scope here).
- `refine_intent(query: str, history: Sequence[str], llm_client: LLMClient, *,
  model=None, temperature=0.0, max_tokens=None, timeout=None) -> IntentRefinerResult`
  -- builds a prompt, calls `llm_client.complete(...)`, parses the raw completion
  string as JSON via `_parse_intent_json`, returns `IntentRefinerResult(refined_intent,
  entities, query_type)`.
- `IntentRefinerResult` is a frozen dataclass with `.query_type: QueryType`.
- `IntentRefinerParseError` raised for malformed LLM output (not this subtask's focus).

## Existing test file (`agents/query/test_intent_refiner.py`, unmodified, read-only)
- Defines `_FakeLLMClient(LLMClient)` -- a real ABC subclass (not `MagicMock`), taking
  a canned `response: str | None` or `error: Exception | None`, recording `.calls`.
- Defines `_well_formed_json(refined_intent, entities, query_type) -> str` helper
  producing the canned JSON string `_FakeLLMClient` returns.
- Already covers ONE `factual_lookup` fixture ("what's the total on invoice 4521?")
  and ONE `broad_exploratory` fixture ("tell me about our billing disputes"), plus
  history-influence, kwargs-forwarding, code-fence tolerance, and malformed-output/
  error-propagation cases.
- Its own docstring (lines 12-15) explicitly defers "query_type classification
  accuracy across many fixture variants" to 4.3.2's file -- confirming no overlap
  is expected/desired between the two files' *purpose*, even though both files
  necessarily exercise the same `refine_intent()` API surface.

## Design decision for the new file
Reuse the exact same mocking pattern (`_FakeLLMClient`, `_well_formed_json`-style
helper) for consistency with the sibling file and the repo's established
`ingestion/test_segment.py` precedent (per 4.3.1's own docstring/handoff), but use
a **distinct, non-overlapping set of fixture query strings** (different domains:
factual lookup questions with a single concrete answer vs. broad/exploratory
requests for overviews/summaries/histories) so this file is additive coverage, not
a duplicate of 4.3.1's two fixtures. Parametrize with `pytest.mark.parametrize` to
keep the file compact while covering >= 3 fixtures per category (>= 6 total),
satisfying "verified across multiple fixture queries" for BOTH categories.

## No production code changes
`intent_refiner.py` is untouched. No new imports needed beyond what 4.3.1's test
file already imports (`llm.client.LLMClient`, `query.intent_refiner.*`, `json`,
`pytest`).
