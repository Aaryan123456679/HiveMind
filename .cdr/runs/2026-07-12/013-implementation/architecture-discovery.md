# Architecture Discovery — Subtask 6.1.2

## `/query` API contract (source of truth: `api/routes/query.go`)

- Request (`QueryRequest`, JSON body, POST only):
  ```json
  { "query": "string", "history": ["string", "..."] }  // history optional
  ```
- Response (`QueryResult`, JSON body, 200 OK):
  ```json
  { "answer": "string", "citations": ["file/path/one.md", "..."] }
  ```
  Field names are `answer` (synthesized answer text) and `citations` (array of cited file path
  strings) — mirrors `agents/query/pipeline.py`'s `QueryPipelineResult.synthesis`
  (`SynthesizerResult.answer` / `.citations`).
- Errors: 405 for non-POST, 400 for malformed JSON or empty `query` (after trim), 500 (plain
  text body = error message) if the pipeline errors. `api/routes/query.go`'s own doc comment
  discloses that the pipeline itself is not yet wired end-to-end (`QueryPipeline` interface is
  a seam awaiting a later subtask's gRPC/HTTP client) — this does not block the UI subtask,
  which only needs to conform to the documented request/response JSON shape and handle the
  documented error statuses generically.
- `docs/HLD.md` section 3.1 confirms `api/` exposes `/query` (among others) as the Go HTTP
  gateway route; `docs/LLD/query-agent.md` describes the intent-refiner/topic-selector/
  synthesizer pipeline behind it but does not add any additional wire-format detail beyond
  what `api/routes/query.go` already fixes.

## Existing `ui/` conventions (subtask 6.1.1)

- `ui/src/routes/QueryView.tsx` (placeholder): function component, imports a stub
  `fetchQueryResult()` from `../api/mockClient`, calls it in a `useEffect` on mount, renders a
  `data-testid="query-status"` status line. This whole approach is documented as a stand-in to
  be replaced by 6.1.2.
- `ui/src/api/mockClient.ts`: **no real HTTP client exists anywhere in `ui/src/`** — every
  exported function is a placeholder Promise that never touches `fetch`/XHR (explicit comment:
  "does not touch `fetch`/XHR. Real request/response shapes will be filled in by the subtask
  that builds each view's real functionality."). Confirms this subtask must introduce the first
  real HTTP call in `ui/`. There is no established axios/fetch wrapper, no `VITE_API_BASE`/env
  convention, and no API base URL configuration anywhere in `ui/` (checked `vite.config.ts`,
  `package.json`, no `.env*` files). Deploy wiring (`deploy/`) is explicitly "not yet built" per
  `docs/HLD.md` section 4, so there's no reverse-proxy/base-path contract to match either.
  Decision (disclosed): call the browser `fetch` API directly against the relative path
  `/query` (same-origin), consistent with `api/routes/query.go` registering the handler at
  exactly `/query` with no path prefix — this keeps the seam simple and matches what a later
  dev-proxy/deploy subtask would need to satisfy. No new library dependency is introduced.
- `ui/src/App.tsx` / `ui/src/App.test.tsx` (subtask 6.1.1 router test): `App.test.tsx`'s
  `it.each(routes)` loop currently expects **every** route's view to expose a
  `data-testid="query-status"`-style element that appears asynchronously after a mocked
  `../api/mockClient` promise resolves on mount. Because 6.1.2 removes the on-mount
  `fetchQueryResult()` call from `QueryView` entirely (querying is now user-triggered via
  submit, not automatic on mount), the `/query` router-test row in `App.test.tsx` needs a
  small, targeted update so it asserts against an element that's actually present after this
  change (the query form, always rendered) instead of the old on-mount status testid. This is
  a necessary dependent-file edit to keep 6.1.1's already-verified test green, not a
  re-implementation of 6.1.1's scope.
- Test tooling already in place: Vitest + `@testing-library/react` + `@testing-library/jest-dom`
  (`ui/src/setupTests.ts`), `vi.mock(...)` used in `App.test.tsx` to replace `../api/mockClient`
  module exports — this subtask's `QueryView.test.tsx` follows the same `vi.mock` convention,
  but mocks the global `fetch` (since the real implementation now calls `fetch` directly, not
  `mockClient`) rather than a client module.
- Citations rendering as "clickable": `ui/src/routes/FilesView.tsx` is the app's `/files` route
  (per `docs/HLD.md`/issue #30, 6.1.4 will build out real file-catalog browsing there). The
  natural interpretation of "clickable ... cited file paths" here is a client-side
  `react-router-dom` `Link` from each citation to `/files?path=<citation>` so a user can jump
  toward that file's entry in the (future) files view — `react-router-dom` is already a
  dependency, no new library needed.

## Files read

- `api/routes/query.go` (full contract + doc comments)
- `docs/HLD.md` (section 3.1 route list, section 4 repo layout / "not yet built" deploy note)
- `ui/src/routes/QueryView.tsx`, `ui/src/api/mockClient.ts`, `ui/src/App.tsx`,
  `ui/src/App.test.tsx`, `ui/src/setupTests.ts`, `ui/package.json`, `ui/vite.config.ts`
