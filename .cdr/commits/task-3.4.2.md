# task-3.4.2 — Candidate topic shortlisting via SearchCandidates + local BM25 (agents/ingestion/)

**Issue:** #18 ("[3] Segmentation agent", milestone #5 "Phase 3")
**Subtask:** 3.4.2 (2 of 6)
**State:** verified
**Verdict:** PASS_WITH_COMMENTS

## Summary

Second of six subtasks under GitHub issue #18 (segmentation agent, milestone #5, "Phase 3"). Adds
`agents/ingestion/shortlist.py`: `shortlist(document_text, search_candidates, *, top_k=8,
pool_size=200)`, which pulls a bounded candidate pool from the engine's `SearchCandidates` RPC and
re-ranks it locally with a pure-Python Okapi BM25 implementation, returning at most `top_k`
`TopicCandidate` results. This is the shortlisting step later subtasks in the issue (the
segmentation agent itself, 3.4.3; `ProposeSplit`, 3.4.5) will consume to bound prompt size when
asking an LLM to place a document against the existing topic catalog. Purely additive — no
existing module modified. Independently verified PASS_WITH_COMMENTS, no fix cycle needed.
**Issue #18 has 4 subtasks remaining (3.4.3–3.4.6); NOT ready to close.**

## Features

- `shortlist()` (`shortlist.py`): calls the injected `search_candidates` callback once for a
  bounded pool (default `pool_size=200`), then re-ranks the pool locally against `document_text`
  via Okapi BM25 and truncates to `top_k` (default 8). Result size is always
  `<= min(top_k, pool_size, len(pool))`.
- `TopicCandidate` (dataclass: `file_id`, `path`, `score`) and `SearchCandidatesFn` (type alias) —
  the injection point that decouples `shortlist()` from any specific transport.
- `GrpcSearchCandidatesClient`: a new, minimal real gRPC-backed implementation of
  `SearchCandidatesFn`. No pre-existing gRPC client wrapper existed anywhere in `agents/` before
  this change (confirmed via repo-wide grep). Lazily imports the generated `hivemind_pb2`/
  `hivemind_pb2_grpc` modules inside `__init__`/`__call__` rather than at module import time, with
  a `Path(__file__).resolve().parent.parent`-based `sys.path` fallback that was independently
  confirmed CWD-independent (constructed and invoked successfully from `/tmp`, a different CWD
  than `agents/`).
- `_bm25_scores`: pure-Python Okapi BM25 over the candidate pool, `O(pool_size *
  unique_query_terms)` with a single doc-frequency pre-pass. No new dependency added — confirmed
  `agents/pyproject.toml` diff for this commit is empty.
- Division-of-labor finding (confirmed, not just claimed): `SearchCandidates`
  (`engine/rpc/server.go:302-329`) is genuinely a plain btree path-prefix scan
  (`btree.PrefixScan`, `strings.HasPrefix`-based, early-exiting) that assigns every result the
  same constant placeholder score (`searchCandidateScore = 1.0`, `server.go:287`) — it performs no
  real content ranking. All actual relevance ranking happens locally in `shortlist.py` via BM25.
  This call/local-rank split was independently verified against the Go source and judged
  architecturally sound: the engine stays a cheap bounded-pool fetch, and ranking logic lives in
  one place (Python) rather than being duplicated or half-implemented on both sides.
- `agents/ingestion/test_shortlist.py`: 16 tests covering normal ranking, `top_k=0`, empty pool,
  negative `top_k`/`pool_size` (raises `ValueError`), pool smaller than `top_k`, and mocking
  `SearchCandidates` via a plain callable/`MagicMock` (no real `grpc.Channel` or socket opened
  anywhere in the suite).

## Impact

- `agents/ingestion/segment.py` (3.4.3) and `agents/ingestion/propose_split.py` (3.4.5) — neither
  built yet — now have a real, tested shortlisting step to bound the candidate set they pass to an
  LLM, instead of needing to call `SearchCandidates` and rank results themselves.
- No existing files modified besides the two new module files; `engine/` (Go side) confirmed
  untouched via `git show --name-status` for this commit.
- Full `agents/` regression suite (`pytest agents/ -q`) run 3x independently: 83 passed each time,
  0 flakiness. Targeted suite: 16/16 passed. `ruff check` clean.
- One non-blocking finding, disclosed and recorded (not a functional defect): the module
  docstring's quote of `docs/LLD/ingestion-agent.md` is a fabricated composite — it stitches two
  non-adjacent LLD lines (30-36 and 76-77) into a single quoted string and substitutes the LLD's
  actual wording ("a cheap embedding/BM25 pre-filter against the existing catalog, via a local
  embedding model, e.g. `nomic-embed-text`") with "a cheap local heuristic," which overstates how
  explicitly the LLD endorses this exact pure-BM25/`SearchCandidates`-split design. The
  implementation choice itself remains compliant — issue #18's own acceptance criteria explicitly
  permit BM25 as an alternative to embeddings — but the docstring should not present the
  substituted wording as a direct LLD quote. Recorded in `.cdr/index/regression.jsonl` and
  `.cdr/memory/pending.md`; forward-referenced to milestone #10 per standing convention, no
  dedicated GitHub issue created for it now.
- A separate housekeeping fix (this commit's follow-up) filled in `git_commit` in
  `.cdr/runs/2026-07-10/003-implementation/handoff.json`, which the implementation agent had
  intentionally left `null` pending this close-out, per repo convention.

## Verification

- **Run ID:** `.cdr/runs/2026-07-10/004-verification`
- **Verdict:** PASS_WITH_COMMENTS
- Dimensions: `requirements_conformance` (PASS), `architecture_conformance` (PASS_WITH_COMMENTS —
  `SearchCandidates`/BM25 split confirmed sound; fabricated composite LLD quote noted),
  `regression_risk` (PASS — 83/83 x3, no flakiness), `edge_cases` (PASS — `top_k=0`, empty pool,
  negative inputs, pool-smaller-than-`top_k`, and a 5000-item pool all independently hand-verified),
  `security` (PASS — lazy gRPC import confirmed CWD-independent, no swallowed `ImportError`),
  `performance` (PASS — no quadratic blowup, ~0.2s even at 5000-item pool), `maintainability`
  (PASS_WITH_COMMENTS — same docstring finding), `test_coverage` (PASS — 16/16, adversarial
  fixture independently confirmed correct ranking), `confidence` (HIGH — all six implementer
  claims independently re-derived from source, not taken at face value).
- Zero must-fix findings.

## Release Notes

- Added `agents/ingestion/shortlist.py` (`shortlist()`, `TopicCandidate`, `SearchCandidatesFn`,
  `GrpcSearchCandidatesClient`, `_bm25_scores`) and `agents/ingestion/test_shortlist.py`: bounded
  candidate-pool fetch from the engine's `SearchCandidates` RPC, re-ranked locally via Okapi BM25,
  for downstream segmentation-agent prompt construction.
- Non-blocking follow-up noted: `shortlist.py`'s module docstring's composite quote of
  `docs/LLD/ingestion-agent.md` should be corrected to either quote verbatim or clearly mark as
  paraphrase, and should be explicit that choosing BM25 over the LLD's suggested embedding model
  was a deliberate, issue-permitted deviation rather than something the LLD itself endorses.
  Forward-referenced to GitHub milestone #10 (run-metadata local-only, no dedicated issue).
- Issue #18 (segmentation agent, milestone #5 "Phase 3") now has 2 of 6 subtasks verified
  (3.4.1, 3.4.2); 4 subtasks remain (3.4.3–3.4.6). Not ready to close. All commits local-only, not
  pushed; no GitHub issue/milestone state changed.
