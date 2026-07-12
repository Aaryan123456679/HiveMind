# Plan — subtask 6.1.3 (GraphView)

## Decisions

1. **Wire contract (disclosed, since no Go handler exists yet — see architecture-discovery.md):**
   `GET /graph?path=<encodeURIComponent(path)>` ->
   `200 { "path": string, "neighbors": [{ "path": string, "edgeType": string, "weight": number, "hop": number }] }`
   4xx/5xx -> plain-text error body (same convention as `query.go`/`QueryView.tsx`).
   Documented in a `GraphView.tsx` header comment exactly like `QueryView.tsx` disclosed its
   own real-wiring gap, so a future subtask implementing the real Go handler has a concrete
   seam to match (or a documented reason to change it).

2. **File selection mechanism:** `useSearchParams` (react-router-dom, already a dependency) to
   read an optional `?path=` param for deep-linking (e.g. a future `FilesView` link would point
   at `/graph?path=<path>`, mirroring `QueryView`'s existing `/files?path=...` citation links).
   Additionally render a manual text input + "View graph" submit button (`GraphView` has no
   file picker to depend on yet, since 6.1.4/FilesAdminView doesn't exist), so the view is
   independently usable and testable without deep-linking. Submitting the form updates the
   `?path=` search param (keeps URL shareable) and triggers the fetch.

3. **Visualization approach:** no third-party graph-viz library (none in `package.json`,
   consistent with architecture-discovery.md's finding). Render:
   - A "center" node showing the selected file's path.
   - An SVG connector column + a list of neighbor rows, one per neighbor, each showing target
     path, edge type, weight, and hop count, with a `data-testid="graph-neighbor"` per row so
     the test spec's "assert the expected number of neighbor nodes render" is a simple
     `getAllByTestId("graph-neighbor")` length assertion.
   - This is "real DOM/SVG rendering of nodes and edges" per the task's guidance: testable,
     no snapshot-only opaque canvas.

4. **State machine:** mirror `QueryView.tsx`'s `"idle" | "loading" | "error" | "success"`
   pattern verbatim for consistency.

5. **data-testid convention** (mirrors QueryView's `query-*` naming):
   - `graph-form` (the path-entry form)
   - `graph-path-input`
   - `graph-loading`
   - `graph-error`
   - `graph-result` (wrapper once loaded)
   - `graph-center-path`
   - `graph-neighbors` (the list container)
   - `graph-neighbor` (one per row — the count-assertable element)
   - `graph-no-neighbors` (empty-state message)

## Ordered changes

1. Rewrite `ui/src/routes/GraphView.tsx`:
   - Drop the `fetchGraphNeighbors` mock import/usage.
   - Add `useSearchParams`, local `pathInput` state initialized from `?path=` if present.
   - `useEffect`: if `?path=` is present on mount, auto-fetch for that path.
   - `handleSubmit`: trim input, no-op on empty (mirrors QueryView's empty-query guard), set
     `?path=` search param, call `fetch(`/graph?path=${encodeURIComponent(path)}`)`.
   - Render form + loading/error/result states per the data-testid list above.
   - Empty-neighbors case renders `graph-no-neighbors` message instead of an empty list.
2. Add `ui/src/routes/GraphView.test.tsx`:
   - Mirrors `QueryView.test.tsx` structure: `vi.stubGlobal("fetch", vi.fn())` in `beforeEach`,
     `vi.unstubAllGlobals()` / `vi.restoreAllMocks()` in `afterEach`.
   - Test 1 (primary acceptance test): mock a `/graph` response with N (3) neighbors, type a
     path, submit, assert `fetch` called with `GET /graph?path=<encoded>`, assert
     `screen.getAllByTestId("graph-neighbor")` has length 3, assert center path and one
     neighbor's fields render.
   - Test 2: deep-link via `?path=` initial route (`MemoryRouter initialEntries`) auto-fetches
     without requiring form submission.
   - Test 3: error path (`ok: false`) renders `graph-error`.
   - Test 4: empty neighbors array renders `graph-no-neighbors`, not a neighbor list.
3. Leave `ui/src/api/mockClient.ts`'s `fetchGraphNeighbors` export in place (still imported by
   nothing after this change is fine — the module doc comment already anticipates each view
   "outgrowing" its stub one at a time as 6.1.2/6.1.3/6.1.4 land).
4. Run `npm run build` and `npm test` inside `ui/` (self-consistency step only, not
   verification).
5. One commit, Problem/Solution/Impact style matching this repo's recent commits.
