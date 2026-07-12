# Architecture Discovery -- Subtask 6.1.5

## Route list (from `ui/src/App.tsx`, lines 19-41)

Not wrapped in `<BrowserRouter>` itself -- that's done once in `ui/src/main.tsx`
(`<BrowserRouter><App /></BrowserRouter>`), so a real browser session hitting any of these
paths and using in-page `<NavLink>`/`<Link>` navigation is representative of production
routing:

- `/` -> redirects to `/query`
- `/ingest` -> `IngestView` (still a 6.1.1 scaffold placeholder, out of scope for this smoke test)
- `/query` -> `QueryView` (6.1.2)
- `/graph` -> `GraphView` (6.1.3)
- `/files` -> `FilesAdminView` (6.1.4)
- `/admin` -> `FilesAdminView` (same component instance as `/files`, per 6.1.4's
  consolidation decision)

Nav bar: `<nav aria-label="primary">` with `<NavLink>`s to Ingest/Query/Graph/Files/Admin
(no explicit `data-testid`, but `aria-label`/link text selectors are stable).

## QueryView.tsx -- fetch contract + testids

- `POST /query` with body `{ "query": string }` -> `{ "answer": string, "citations": string[] }`.
- Testids: `query-form` (form), `#query-input` / `aria-label="Query"` (input), submit
  button (text "Submit"), `query-loading`, `query-error` (role=alert), `query-result`,
  `query-answer`, `query-citations` (ul) / `query-no-citations`.
- Citation links point at `/files?path=<encodeURIComponent(path)>`.

## GraphView.tsx -- fetch contract + testids

- `GET /graph?path=<encodeURIComponent(path)>` -> `{ "path": string, "neighbors": [{ "path", "edgeType", "weight", "hop" }] }`.
- Supports a `?path=` deep link (auto-fetches on mount) as well as a manual form.
- Testids: `graph-form`, `graph-path-input`, submit button (text "View graph"),
  `graph-loading`, `graph-error`, `graph-result`, `graph-center-path`, `graph-neighbors`,
  `graph-connectors` (svg), `graph-neighbor` (li, repeated), `graph-neighbor-path`,
  `graph-neighbor-type`, `graph-no-neighbors`.

## FilesAdminView.tsx -- fetch contract + testids

- `GET /files` -> `{ "files": [{ "path", "status", "sizeBytes", "lastModified" }] }`.
- `GET /admin` -> `{ "fileCount": number, "statusCounts": { ACTIVE, SPLITTING, SPLIT, REDIRECT } }`.
- Both fetches fire in parallel on mount (`useEffect` with no deps) for both the `/files`
  and `/admin` routes (same component instance).
- Testids: `admin-loading`, `admin-error`, `admin-result`, `admin-file-count`,
  `admin-status-counts`, `admin-status-count-<STATUS>`; `files-loading`, `files-error`,
  `files-result`, `files-catalog-row` (li, repeated, `data-highlighted` attr),
  `files-catalog-row-path`, `files-catalog-row-status`, `files-no-entries`.
- Reads `?path=` search param to highlight/scroll a matching row (not required for this
  smoke test's assertions, but means `/files` and `/admin` mock responses must be
  provided together since both fetch on every mount of this component, whether reached
  via `/files` or `/admin` route).

## Existing devDependencies / tooling (`ui/package.json`)

No Playwright, no Cypress, no e2e tooling of any kind. Existing test tooling is
`vitest` + `@testing-library/react` (component/unit level only, jsdom-based, no real
browser, no real network layer -- `App.test.tsx` wraps `App` directly in `MemoryRouter`
and mocks `global.fetch`). This subtask is confirmed to be the *first* introduction of
browser-level e2e tooling in `ui/`, per issue #30's own subtask ordering.

`ui/vite.config.ts` and `ui/vitest.config.ts` exist as separate config files already
(vite for build/dev, vitest for the component-test config) -- a new `playwright.config.ts`
at the `ui/` root follows this repo's existing convention of one config file per tool
rather than merging into `vite.config.ts`.

`ui/src/main.tsx` mounts via `ReactDOM.createRoot` at `#root` in `ui/index.html`, wrapped
in `<React.StrictMode><BrowserRouter>`.

## e2e conventions in other language stacks (for spirit, not code reuse)

- `agents/ingestion/test_e2e_smoke.py` and `agents/query/test_query_e2e.py`: Python
  pytest-based e2e/integration tests. Convention: name clearly signals "e2e" or
  "smoke" scope in the filename; module docstring explicitly discloses what "end-to-end"
  does and does not cover (e.g. `test_query_e2e.py`'s docstring: LLM completions and gRPC
  boundary are faked, but every other real pipeline step runs unmocked against real
  on-disk files) -- i.e. this repo's established convention is to be explicit and honest
  in-file about what is mocked vs. real, rather than silently mixing fakes into a test
  that reads as fully "real."
  - Applying that convention here: `ui/e2e/smoke.spec.ts` must explicitly disclose, in
    its own header comment, that the backend is mocked via Playwright network-route
    interception (since no real `/graph`/`/files`/`/admin` Go handlers exist yet), matching
    the *shape* of the contracts defined in 6.1.2-6.1.4's own header comments -- not a
    real backend process.
- No existing e2e convention exists in `docs/HLD.md` or `docs/LLD/` specific to a browser
  UI; `docs/LLD/ingestion-agent.md` and `docs/LLD/rpc.md` only use "end-to-end" in prose,
  not as a formal UI test convention to mirror.

## Tooling choice inputs

- Environment has outbound network access (`npm ping` succeeds against
  `registry.npmjs.org`), so installing a new devDependency and downloading a Playwright
  browser binary is expected to be feasible, pending confirmation in the self-consistency
  step.
- Node v22.22.3, npm 10.9.8 -- compatible with current Playwright and Cypress major
  versions.
