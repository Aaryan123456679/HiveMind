# Impact Analysis — subtask 4.5.17.5

## Files touched
- NEW: `agents/query/conftest.py` — shared `FakeLLMClient`, `topic()`, `neighbor()`,
  `RecordingGraphNeighbors`.
- `agents/query/test_intent_refiner.py` — removed local `_FakeLLMClient`; imports shared
  `FakeLLMClient` aliased as `_FakeLLMClient` (keeps every existing call-site/reference in
  the file unchanged).
- `agents/query/test_intent_refiner_types.py` — same treatment; this file only ever
  constructed `_FakeLLMClient(response=...)` positionally (never used `error=`), so the
  superset shared class is a strict behavioral no-op for it.
- `agents/query/test_topic_selector_expansion.py` — removed local
  `class _RecordingGraphNeighbors`; imports shared `RecordingGraphNeighbors` aliased as
  `_RecordingGraphNeighbors`.
- `agents/query/test_topic_selector_cap.py` — removed local `_topic`/`_neighbor`
  functions; imports shared `topic`/`neighbor` aliased as `_topic`/`_neighbor`.
- `agents/query/test_topic_selector_integration.py` — removed local `_topic`/`_neighbor`/
  `_RecordingGraphNeighbors`; imports all three shared, aliased to original names.

## No production code changes
`agents/query/intent_refiner.py`, `agents/query/topic_selector.py`, and all other
non-test modules are untouched. This is a pure test-fixture consolidation refactor.

## Risk assessment
- Low risk: aliasing (`_topic = topic`, etc.) at module scope preserves every existing
  call site verbatim, so the only way behavior could regress is if the shared
  implementations differ semantically from an original — checked each one:
  - `FakeLLMClient`: superset of both originals (adds `error` support that
    `test_intent_refiner_types.py` never used) — safe.
  - `topic()`/`neighbor()`: identical bodies to both originals; the only difference
    between the two originals themselves was `test_topic_selector_integration.py`'s
    `_topic` requiring `score` positionally (no default) vs. `test_topic_selector_cap.py`'s
    default `score=1.0` — the shared version keeps the default, and
    `test_topic_selector_integration.py` always passed `score` explicitly, so this is a
    no-op.
  - `RecordingGraphNeighbors`: byte-for-byte identical logic to both originals.

## Cross-package effects
None — no other package imports from `agents/query/test_*.py` files or the new
`conftest.py`.
