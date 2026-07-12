# Plan — Subtask 6.1.1

## Framework decision

Vite + React 18 + TypeScript + react-router-dom, Vitest + @testing-library/react for tests.
Rationale in architecture-discovery.md (repo's own `ui/README.md` names "Vite/React"; no
existing package.json to conflict with; Vitest avoids a second bundler/test-runner config
since it shares Vite's config/transform pipeline).

## Ordered changes

1. Scaffold Vite React-TS project files by hand (no network `npm create vite` needed —
   write `package.json`, `tsconfig.json`, `tsconfig.node.json`, `vite.config.ts`,
   `vitest.config.ts`, `index.html`, `ui/.gitignore` directly).
2. `npm install` inside `ui/` to materialize `node_modules` + `package-lock.json`.
3. `ui/src/main.tsx` — React root render entrypoint mounting `<App />`.
4. `ui/src/App.tsx` — `BrowserRouter` (Vitest test uses `MemoryRouter` around the same
   `AppRoutes`/`App` component, see step 6) wiring a `<Routes>` with the five paths:
   - `/ingest` -> `IngestView`
   - `/query` -> `QueryView`
   - `/graph` -> `GraphView`
   - `/files` -> `FilesView`
   - `/admin` -> `AdminView`
   Plus a redirect from `/` to `/query` (sensible default landing route) and a minimal nav.
5. `ui/src/routes/{IngestView,QueryView,GraphView,FilesView,AdminView}.tsx` — minimal
   placeholder components (heading + one-line description of what the real subtask will add),
   each importing `ui/src/api/mockClient.ts` and calling a stub fetch function so the "mocked
   API responses" seam already exists structurally for 6.1.2-6.1.4 to fill in, without this
   subtask doing any real fetching itself.
6. `ui/src/api/mockClient.ts` — trivial placeholder module (typed stub functions returning
   resolved promises) that route components import; this is what the router test mocks via
   `vi.mock`, satisfying "render without error using mocked API responses" without requiring
   this subtask to build real API wiring.
7. `ui/src/App.test.tsx` — the router test: renders `<App />` wrapped in `MemoryRouter`
   (via `initialEntries`) once per route, asserting each of the 5 routes renders its
   corresponding view's marker text without throwing; mocks `ui/src/api/mockClient` with
   `vi.mock` so no real network call is attempted.
8. Update `ui/README.md` to reflect the scaffold now existing (drop "to be added" wording,
   note `npm run build` / `npm test` commands).
9. Run `npm run build` and `npm test` locally (self-consistency step, not verification).
10. One git commit covering only `ui/` changes.

## Explicitly deferred (later subtasks / not this commit)

- Real `/query` submit-and-render flow (6.1.2).
- Real graph visualization (6.1.3).
- Combined `FilesAdminView.tsx` replacing the two separate `FilesView`/`AdminView`
  placeholders, with real catalog/stats data (6.1.4).
- e2e Playwright/Cypress smoke test (6.1.5).
- Wiring the UI to the real `api/` Go gateway (still has a disclosed `/query` implementation
  gap per api/routes/query.go; out of scope here regardless).
