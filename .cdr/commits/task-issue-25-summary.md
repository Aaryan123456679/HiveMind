# Issue #25 — Full query-pipeline wiring + end-to-end test (Phase 4: Query pipeline)

## Summary

Issue #25 delivers the final wiring step of the query pipeline: a single
chaining function that composes `intent_refiner` → `topic_selector` →
`synthesizer` (issues #22/#23/#24) in order, exposes it via `api/`'s
`POST /query` HTTP route behind a DI seam, and proves the chain works
end-to-end against a real on-disk corpus with real citation resolution.
Both subtasks are implemented and independently CDR-verified
**PASS_WITH_COMMENTS**:

- **4.6.1** — `run_query_pipeline()` chaining all three query agents,
  exposed via `api/routes/query.go`'s `/query` route and registered in
  `api/main.go`.
- **4.6.2** — `test_query_e2e.py`, an end-to-end test that seeds a real
  3-file markdown corpus on disk and runs the full chain for real (no
  pipeline-internal step mocked), asserting both a valid citation resolves
  to a real seeded file and a hallucinated citation is correctly flagged.

Together these close out **issue #25**, the last issue under **milestone
#6 "Phase 4: Query pipeline"** (issues #20–#25). With this issue closed,
Phase 4 is **functionally complete at the local-commit level**, pending a
separate milestone-close step.

Both subtasks' verifications independently reached the same judgment: issue
#25's acceptance criteria are satisfied **under a disclosed DI-seam scope
boundary** — the pipeline's integration points (`SearchCandidates`,
`GraphNeighbors`, `GetFile`) are exercised as injected callables, not real
gRPC/HTTP network calls, because no client-side invocation of those RPCs
exists anywhere in `agents/query/` or `api/` yet (a pre-existing gap
disclosed since #21/#23, not introduced by this issue). 4.6.2's own
verification explicitly weighed and rejected holding the e2e subtask to a
*stricter* standard than 4.6.1 was already held to, since the DI seam is the
only integration boundary `agents/query/` exposes today.

## Features

- `agents/query/pipeline.py` — `run_query_pipeline()`: chains
  `refine_intent` → `select_top_k` → `expand_insufficient_topics` →
  `combine_and_cap` → `get_file` (per selected file) → `synthesize_answer`,
  with `search_candidates`/`graph_neighbors`/`get_file` injected as
  callables (matching the established DI convention across `agents/query/`).
  Every call site's positional/keyword arguments were verified byte-for-byte
  against the real, already-verified upstream function signatures.
- `api/routes/query.go` — a `POST /query` HTTP handler behind a
  `QueryPipeline` Go interface (the DI seam this issue's test spec mocks at
  the "gRPC boundary"); `api/main.go` registers it and the server now
  actually listens on this route.
- `agents/query/test_query_e2e.py` — seeds 3 real, distinct-content markdown
  files on disk (`tmp_path`) and runs `run_query_pipeline()` for real, with
  only `LLMClient` and the `search_candidates`/`graph_neighbors`/`get_file`
  DI-seam callables faked (the fakes perform real disk I/O and real
  keyword-overlap scoring, not canned lookups). Two assertions: a valid
  citation resolves to a real seeded file (`unknown_citations() == []`), and
  a hallucinated citation is correctly flagged while the real citation is
  not. No production code modified by 4.6.2 — confirmed via direct diff
  (`git diff 00e5d9c f660ab8 -- agents/query/*.py` = 0 lines changed), not
  just the commit message's claim.

## Impact

- `/query` is now a real, reachable HTTP route chaining all three query
  agents in the documented order (`docs/LLD/query-agent.md`'s "Pipeline
  order" section). This is the last piece of `agents/query/`'s LLD-named
  surface to be wired together.
- The full chain — refined intent → top-k/graph-expanded file selection →
  citation-annotated synthesized answer — is now proven correct against
  genuine on-disk file content and genuine citation-resolution logic, not
  just isolated unit tests with hardcoded fixtures.
- **Explicit disclosed gap, not a subtask of this issue:** real gRPC/HTTP
  wiring for `/query` does not exist yet. Every real request to `/query`
  today returns a 500 via a `notImplementedPipeline` stand-in in
  `api/main.go`, because `proto/hivemind.proto` has no RPC for invoking the
  Python query pipeline from `api/`, and no client-side invocation of
  `SearchCandidates`/`GraphNeighbors`/`GetFile` exists in `agents/query/`
  either. This gap was disclosed at 4.6.1's own commit time and independently
  confirmed as an accepted scope boundary by both subtasks' verifications —
  **it should be tracked as a new issue**, recommended under milestone #10
  "Phase 4.5: Storage-engine technical debt" (or a fresh standalone issue if
  preferred). This step does not create that issue.
- A related latent gap for that future real-wiring work: `GetFileFn`'s
  `(path, content)` return shape has no field-level counterpart in the real
  `GetFileResponse` proto message (which carries `content` + `version` only,
  no `path`). Whoever wires `get_file` for real should source `path` from
  `TopicCandidate.path` (already carried through selection) rather than
  expecting `GetFileResponse` to supply it.
- 4.6.2's e2e test uses `k=1`, which by `topic_selector.is_insufficient_alone`'s
  own documented invariant means the single selected topic can never be
  judged insufficient relative to itself — so `expand_insufficient_topics`
  is structurally never exercised by this e2e test against the real seeded
  corpus. That branch remains covered only by `test_pipeline.py`'s `k=2`
  case, which uses a hardcoded in-memory fixture asserting call
  order/arguments, not citation resolution. No test in the repository today
  combines "real corpus + graph expansion + citation resolution" — this is a
  genuine, disclosed test-coverage gap, not a blocker.
- Both `00e5d9c` (4.6.1) and `f660ab8` (4.6.2) commit messages already
  follow the Problem/Solution/Impact standard — no deviation to note, no
  git history rewrite needed.
- Full regression suites clean throughout: Python (`pytest . --ignore=
  ingestion/test_e2e_smoke.py`) at 291 (4.6.1) and 293 (4.6.2) passed, with
  the same 2 pre-existing, unrelated protobuf gencode/runtime version-mismatch
  failures (tracked as issue #46) present before, during, and after this
  work; Go (`api/` + `engine/` modules) all pass; `ruff check agents/query/`
  and `gofmt`/`go vet` on `api/` clean throughout.
- Consistent with issues #20–#24 precedent: both commits are local-only
  (not pushed), and issue #25 is not being closed on GitHub as part of this
  step — a separate step will handle push/close.

## Verification

| Subtask | Commit | Verdict | Verification run |
|---|---|---|---|
| 4.6.1 | `00e5d9c` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/031-verification` |
| 4.6.2 | `f660ab8` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/033-verification` |

Non-blocking findings carried forward (all previously disclosed in the
verification runs; none are blocking):

- **F-4.6.1-1 — `/query` route returns 500 for every real request today.**
  The `notImplementedPipeline` stand-in in `api/main.go` means the
  acceptance criterion "exposed via `api/`'s `/query` route" is satisfied
  only in the narrow "reachable, structurally wired" sense — an end user
  hitting `POST /query` today always gets a 500 with a not-implemented
  error. This is expected and disclosed, and is **not a subtask of this
  issue**; real gRPC/HTTP wiring is accepted, disclosed future work (see
  Impact above — recommend tracking as a new issue under milestone #10 or
  standalone).
- **F-4.6.1-2 — `GetFileFn`'s `(path, content)` shape has no proto
  counterpart.** `proto/hivemind.proto`'s current `GetFileResponse` carries
  only `content` + `version`, no `path` field. Future real wiring must
  source `path` from `TopicCandidate.path` instead of expecting
  `GetFileResponse` to supply it (or the proto message must be extended).
- **F-4.6.2-1 — expansion branch never exercised against the real seeded
  corpus.** `test_query_e2e.py`'s `k=1` design structurally guarantees
  `graph_neighbors` is never called (a single top topic can never be judged
  insufficient relative to itself, per `is_insufficient_alone`'s own
  documented invariant). `test_pipeline.py`'s `k=2` case covers the
  expansion branch's call order/arguments only, with a hardcoded in-memory
  fixture and no citation-resolution assertion. No test today combines "real
  on-disk corpus + graph expansion + citation resolution." Recommended
  follow-up: either a 3rd e2e test case engineered so one candidate scores
  below the insufficiency ratio threshold at `k=2`, or an explicitly logged
  accepted gap, before treating query-pipeline e2e coverage as fully
  complete.

## Release Notes

- Added `agents/query/pipeline.py`'s `run_query_pipeline()`, chaining
  `intent_refiner` → `topic_selector` → `synthesizer` end to end, and wired
  it up behind `api/`'s new `POST /query` HTTP route
  (`api/routes/query.go`, registered in `api/main.go`).
- Added `agents/query/test_query_e2e.py`, an end-to-end test proving the
  full chain resolves citations correctly against a real on-disk corpus
  (both a valid-citation and a hallucinated-citation case).
- This closes issue #25, the last issue under milestone #6 "Phase 4: Query
  pipeline" (issues #20–#25) — **Phase 4 is functionally complete** at the
  local-commit level, pending a separate milestone-close step.
- **Known, disclosed, non-blocking gap — real `/query` wiring remains
  future work:** `POST /query` currently returns 500 for every real request
  (no gRPC/HTTP client wiring exists yet from `api/`/`agents/query/` to the
  storage engine's `SearchCandidates`/`GraphNeighbors`/`GetFile` RPCs).
  Recommend opening a new issue for this — under milestone #10 "Phase 4.5:
  Storage-engine technical debt," or as a fresh standalone issue — to track
  real wiring, including resolving the `GetFileResponse` path-field gap
  (F-4.6.1-2).
- Known, non-blocking follow-up: add e2e coverage combining the real seeded
  corpus with the `expand_insufficient_topics` branch and citation
  resolution together (currently untested in combination; see F-4.6.2-1).
