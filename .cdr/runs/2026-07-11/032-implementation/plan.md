# Plan -- Issue #25 subtask 4.6.2

## New file: `agents/query/test_query_e2e.py`

### Seeded corpus fixture
`_seed_corpus(tmp_path) -> dict[str, int]` (path -> file_id), writing >=3 real,
distinct-content markdown files under `tmp_path`, e.g.:
- `billing/InvoiceDisputes.md` -- invoice dispute process content.
- `security/ApiKeyRotation.md` -- API key rotation policy content.
- `onboarding/NewHireChecklist.md` -- new-employee onboarding content.

`file_id`s assigned deterministically (enumerate sorted relative paths, 1-based) so
tests can reference them without depending on dict ordering.

### Real (disk-backed), non-network fake callables
- `_make_get_file(tmp_path, id_to_path) -> GetFileFn`: reads the real file off disk
  (`(tmp_path / path).read_text()`) each call -- exercises real I/O, not a canned dict.
- `_make_search_candidates(tmp_path, id_to_path) -> SearchCandidatesFn`: does a real
  (if simple) relevance scan -- reads every seeded file's real content off disk on each
  call and scores by lowercase keyword-overlap count with the query, returning
  `TopicCandidate`s sorted descending by score. Deliberately real file I/O, not a
  hardcoded candidate list, so the corpus is genuinely "searched," not stubbed.
- `_fake_graph_neighbors`: returns `[]` unconditionally; scores are engineered (see
  below) so no topic is ever judged insufficient, meaning this is provably never called
  with a non-empty-selection input in either test -- expansion-path call order is
  already covered end-to-end by 4.6.1's own `test_pipeline.py`, out of re-scope here.
- `_FakeLLMClient(LLMClient)`: same convention as `test_pipeline.py`'s own fake --
  real `LLMClient` ABC subclass, returns canned JSON responses in call order (one for
  intent-refinement, one for synthesis).

### Test 1 -- `test_e2e_valid_citation_resolves_to_real_seeded_file`
- Seed corpus with 3 files.
- Query text overlaps strongly with exactly one seeded file's real content (e.g. an
  invoice-dispute question against the billing file).
- Canned synthesis JSON cites exactly that file's real seeded path.
- Run `run_query_pipeline(...)` for real (no pipeline.py code path mocked).
- Assert: `result.synthesis.citations == [<that real seeded path>]`;
  `result.synthesis.provided_paths` contains that same real path (i.e. it really was
  resolved via `get_file` reading the real corpus, not coincidence);
  `result.synthesis.unknown_citations() == []` (valid citation, resolves to a real
  file that genuinely exists in the seeded corpus).
- Also assert `result.selected_file_ids` are real seeded `file_id`s (subset of the
  corpus's assigned ids) and the synthesis prompt sent to the fake LLM embeds the real
  `"## File: <path>"` header for the resolved file (i.e. the citation traces back to
  real on-disk content, not a hallucinated header).

### Test 2 -- `test_e2e_hallucinated_citation_is_flagged`
- Same real seeded corpus (fresh `tmp_path`, same 3 files).
- Canned synthesis JSON's `citations` list includes both the one real, resolvable
  seeded path AND a fabricated path (`"made/up/NotInCorpus.md"`) that does NOT exist
  anywhere in the seeded corpus / was never passed to the LLM as a `"## File:"` header.
- Run `run_query_pipeline(...)` for real.
- Assert: `result.synthesis.citations` contains both entries as reported by the (fake,
  adversarial) LLM; `result.synthesis.unknown_citations() == ["made/up/NotInCorpus.md"]`
  -- the real file resolves and is NOT flagged, the hallucinated one IS flagged, end-to-
  end through the full pipeline's own citation-resolution logic (not a standalone
  `synthesizer.py` unit test -- this exercises `_build_selected_markdown`'s real
  `provided_paths` derivation from real on-disk `get_file` results, feeding into
  `synthesize_answer`'s real `_extract_provided_paths`/`unknown_citations` logic).

### No production code changes
`pipeline.py`/`synthesizer.py`/`topic_selector.py`/`intent_refiner.py` are not modified.
No bug was found blocking this test.

## Self-consistency (not verification)
1. `cd agents && python3 -m pytest query/test_query_e2e.py -v`
2. `cd agents && python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q` -- expect
   ~294+3=297 passed, 2 pre-existing protobuf failures (issue #46, unrelated).
3. `ruff check agents/query/test_query_e2e.py`

## Commit
One local commit, no push. Message format: `test: <summary>` / `Problem:` / `Solution:`
/ `Impact:`.
