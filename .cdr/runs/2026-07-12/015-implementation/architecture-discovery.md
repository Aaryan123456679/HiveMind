# Architecture discovery — subtask 6.1.3 (GraphView)

## Read order

1. `docs/HLD.md` §3.1/§4 — `api/` gateway routes `/ingest /query /graph /files /admin`;
   `ui/` is React, calls `api/` (not the Go engine or Python agents directly).
2. `docs/LLD/graph.md` — `engine/graph/` adjacency store + `GraphNeighbors(fileID, depth,
   edgeTypeFilter, maxNodes)` traversal API; "Edge shape" section:
   `{ targetFileID, edgeType, weight, lastUpdated }`; `edgeType` in
   `ENTITY_COOCCUR|LLM_ASSERTED|SPLIT_SIBLING|REDIRECT`.
3. `proto/hivemind.proto` — the actual wire-level RPC contract:
   - `GraphNeighborsRequest { file_id, depth (0-2), edge_type_filter, max_nodes }`
   - `Neighbor { target_file_id, type, weight, hop }`
   - `GraphNeighborsResponse { repeated Neighbor neighbors }`
   - Result ordering is documented as "hop asc, weight desc, target fileID asc, then capped at
     max_nodes".
4. `api/routes/` — **only `query.go` exists**. `api/main.go`'s `newMux` calls
   `routes.RegisterRoutes(mux, pipeline)` which registers exactly one route, `/query`
   (`routes.go`'s `RegisterRoutes` → `mux.HandleFunc("/query", ...)`). There is **no** `/graph`
   (nor `/ingest`, `/files`, `/admin`) Go handler anywhere in the repo yet. This is the same
   category of gap `query.go` itself disclosed before subtask 6.1.2 wired a real pipeline behind
   it ("Real-wiring gap" comment in `query.go`) — except here there isn't even a stand-in HTTP
   handler to wire the UI against.
5. `ui/src/routes/GraphView.tsx` (6.1.1 placeholder) — currently only calls
   `fetchGraphNeighbors()` from `ui/src/api/mockClient.ts`, which is a `Promise<MockStatus>`
   stub (`{ ok: true, note: "graph API not yet wired (subtask 6.1.3)" }`), not a real fetch.
6. `ui/src/api/mockClient.ts` — doc comment explicitly says each view "imports one stub function
   from here so later subtasks ... have a stable seam to replace with real fetch calls" — i.e.
   6.1.3 is expected to stop using `fetchGraphNeighbors` from this file and call `fetch`
   directly, exactly as 6.1.2 did for `/query` (see its own doc comment: "This is the first real
   network call in ui/ ... no other HTTP client convention exists yet").
7. `ui/src/routes/QueryView.tsx` + `QueryView.test.tsx` (6.1.2, already verified) — established
   conventions this subtask must stay consistent with:
   - Direct same-origin `fetch("/query", {...})`, no base-URL/client wrapper.
   - `Status = "idle" | "loading" | "error" | "success"` state machine.
   - `data-testid` convention: `<name>-form`, `<name>-loading`, `<name>-error` (with
     `role="alert"`), `<name>-result`, plus one per rendered field.
   - Citations rendered as `<Link to={`/files?path=${encodeURIComponent(citation)}`}>` — i.e.
     the app's file identifier in the URL/UI layer is a **path string**, not a numeric fileID.
   - Test mocks `global.fetch` via `vi.stubGlobal("fetch", vi.fn())` / `vi.unstubAllGlobals()`
     in `afterEach` (no MSW / dedicated fetch-mock library in `devDependencies`).
   - Component wrapped in `<MemoryRouter>` in tests (needed here too, for `useSearchParams`).
8. `ui/src/App.tsx` — routes are simple `<Route path="/graph" element={<GraphView />} />`, no
   route param slot (e.g. no `/graph/:path`); a selected file must be carried via query string
   (`useSearchParams`) or in-component state, consistent with `QueryView`'s citation links
   already pointing at `/files?path=...` (a `?path=` convention already exists in this codebase).
9. `ui/package.json` — vitest 2.1, RTL 16, user-event 14, no graph-viz library
   (react-flow/d3/vis-network etc.) in dependencies — confirms the plan's choice to avoid
   introducing one for this subtask.

## Key findings that shape the plan

- **No real `/graph` HTTP contract exists yet.** This subtask must define one (mirroring the
  proto's `GraphNeighborsRequest`/`Neighbor` field semantics but adapted to the UI's
  path-based identifier convention, since there is no Go handler to consult verbatim and the
  UI never deals in raw fileIDs anywhere else). Decision: `GET /graph?path=<encoded path>`
  returning `{ path: string, neighbors: Array<{ path: string, edgeType: string, weight: number,
  hop: number }> }`. This is a disclosed, deliberate simplification (path instead of fileID)
  for the same reason `QueryView` disclosed its own gap: no Go route exists to reconcile
  against, and every other UI-visible identifier in this codebase (`QueryView`'s citations) is
  already a path string, not a fileID.
- `?path=` query-param convention must be supported for deep-linking (future `FilesView` link
  target), matching `QueryView`'s existing `/files?path=...` pattern.
- Must reuse `QueryView`'s status-machine / data-testid / fetch-mocking conventions verbatim
  for consistency and reviewer familiarity.
- No new dependency needed; a plain list/SVG rendering satisfies "visual adjacency" per the
  test spec's own framing ("asserting the expected number of neighbor nodes render" — implies
  neighbor nodes are discrete renderable elements, not an opaque canvas/library widget).
