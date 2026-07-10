# Plan

1. Resolve F1 (option (a), fix `segment.py` directly):
   - Extract `propose_split.py`'s private `_strip_code_fences`/`_CODE_FENCE_RE` into
     a new shared module `agents/ingestion/_json_fences.py` (`strip_code_fences`).
   - Update `propose_split.py` to import the shared helper instead of owning a
     private copy (pure extraction, behavior unchanged).
   - Update `segment.py`'s `_parse_segment_json` to call `strip_code_fences` before
     `json.loads`, closing F1.
   - Add regression tests in `test_segment.py` (fenced-JSON, both `json`-tagged and
     untagged fence) mirroring `test_propose_split.py`'s existing equivalent test.
2. Build `testdata/notes_corpus/`: 4 short realistic markdown notes spanning
   overlapping topics (billing/invoice-dispute, billing/refund-requests,
   engineering on-call runbook, unrelated HR onboarding) with shared
   entities (Priya Nair, Marcus Webb) and cross-references, usable across
   shortlist/segment/wiring tests.
3. `agents/ingestion/test_segment_fixtures.py` (always runs in CI, mocked):
   - Parametrized structured-output-shape test across three representative
     `RawDocument` source types (`pdf`-stand-in notes, `email`, `ticket`), reusing
     existing `testdata/enron_sample_1.txt` / `testdata/ticket_sample_1.json`.
   - Per-file shape test over every `notes_corpus/*.md` fixture.
   - First end-to-end composition test: `shortlist()` -> `segment()` ->
     `execute_segment()` chained against one real fixture document and a mocked
     `SearchCandidates` pool drawn from the corpus's own topics, each step's real
     output threaded into the next step's real input.
4. `agents/ingestion/test_segment_live.py` (optional, skip-if-unreachable):
   - `_ollama_is_reachable()` short-timeout probe against `OllamaClient`'s default
     base URL; `pytestmark = pytest.mark.skipif(not reachable, ...)` module-wide.
   - Two smoke tests: `segment()` and `propose_split()` against a real
     `OllamaClient`, asserting output shape / partition invariants only (not exact
     content, since a real model's wording is not deterministic).
5. Run full `agents/` pytest suite + ruff; compare failures against a `git stash`
   baseline to confirm no regressions introduced by this change.
6. One local commit; write handoff.json flagging issue #18 fully subtask-complete
   pending verification, and that push/close requires fresh user authorization.
