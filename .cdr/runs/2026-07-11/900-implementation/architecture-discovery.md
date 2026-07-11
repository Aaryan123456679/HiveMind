# Architecture Discovery — subtask 4.5.17.5

## Scope
`agents/query/` test suite (no production code touched).

## Duplicated fixture helpers found

1. `_FakeLLMClient` (LLMClient ABC subclass):
   - `agents/query/test_intent_refiner.py` (lines 36-70, pre-refactor): superset version
     with both `response` and `error` params.
   - `agents/query/test_intent_refiner_types.py` (lines 41-66, pre-refactor): subset
     version, `response`-only, no `error` support.
   - Near-verbatim duplication confirmed; `test_intent_refiner_types.py`'s own docstring
     explicitly acknowledged the duplication was deliberate ("kept as a local copy...
     since agents/query/ has no shared test-helpers module").

2. `_RecordingGraphNeighbors` (GraphNeighborsFn test double):
   - `agents/query/test_topic_selector_expansion.py` (lines 39-49, pre-refactor):
     original/canonical definition.
   - `agents/query/test_topic_selector_integration.py` (lines 55-70, pre-refactor):
     verbatim copy, docstring explicitly says "reused here so the integration test's
     mock boundary matches the convention already established... for 4.4.2".
   - `agents/query/test_topic_selector_cap.py`: does NOT define `_RecordingGraphNeighbors`
     (combine_and_cap has no GraphNeighbors dependency), but DOES define the sibling
     `_topic`/`_neighbor` fixture-builder helpers that are ALSO duplicated verbatim (modulo
     one cosmetic default-arg difference) in `test_topic_selector_integration.py`.

## Existing infra
- No `agents/query/conftest.py` existed prior to this subtask.
- `agents/pyproject.toml` sets `testpaths = ["."]`; pytest auto-discovers `conftest.py`
  files, but a plain module-level import (`from query.conftest import ...`) works too
  since `agents/query` is an importable package (has `__init__.py`) and `agents/` is on
  `sys.path` when running `pytest` from the `agents/` working directory (confirmed via
  `.venv`).

## Baseline test run (pre-refactor)
`cd agents && source .venv/bin/activate && python -m pytest query/ -q` → **103 passed**.
