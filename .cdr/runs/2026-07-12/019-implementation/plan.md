# Plan -- Subtask 6.1.5

## Tooling choice: Playwright (over Cypress)

Chosen: **`@playwright/test`**.

Justification:
- Lighter footprint: single npm package + `npx playwright install` for browser binaries;
  no separate Cypress binary download/GUI runner dependency, no separate Cypress cloud
  account/dashboard concerns.
- First-class network-layer route interception via `page.route(url, handler)`, which is
  exactly the "mocked backend" mechanism this subtask needs (no real Go handlers exist for
  `/graph`, `/files`, `/admin` yet, and `/query` has no seeded backend in a test
  environment either) -- Cypress's `cy.intercept()` is comparable, but Playwright's is
  simpler to compose with a single fixture-per-route map and needs no `cypress.config.ts`
  GUI-oriented scaffolding.
- Runs headless by default in CI-like environments, TypeScript-native config
  (`playwright.config.ts`), integrates cleanly with the existing Vite dev server via
  `webServer` config (auto-starts `npm run dev` / `vite preview` before running tests).
- `@playwright/test` is itself a full assertion/test-runner framework (no separate Jest/
  Mocha needed), keeping the new devDependency surface to one package.

## Backend mocking strategy

No real backend is running in this repo (no Go handlers exist yet for
`/graph`/`/files`/`/admin`; only `/query` is registered per `api/main.go`, and even that
has no standalone runnable server seeded with test data in this environment). Per the
acceptance criteria's own wording ("against a running (or mocked) backend"), this subtask
mocks the backend at the network layer:

- Use Playwright's `page.route()` to intercept the four calls the smoke path touches:
  `POST /query`, `GET /graph?path=...`, `GET /files`, `GET /admin`.
- Each intercepted route fulfills with a hand-written JSON fixture that matches the wire
  contract disclosed in the corresponding view's own header comment (QueryView.tsx,
  GraphView.tsx, FilesAdminView.tsx), so the test exercises real `fetch()` calls, real
  React state transitions, and real DOM rendering -- only the network layer is faked, not
  the component tree (unlike the existing vitest+RTL component tests, which mock
  `global.fetch` in jsdom rather than launching a real browser).
- The Vite dev server itself is launched for real via Playwright's `webServer` config
  (`npm run dev`, i.e. `vite`), so the actual built/served app (real bundler output, real
  browser JS engine, real DOM) is what's driven -- satisfying "loads the app" from the
  acceptance criteria.

## Happy-path script (`ui/e2e/smoke.spec.ts`)

1. Navigate to `/query` (base URL from `playwright.config.ts`'s `use.baseURL`, pointed at
   the Vite dev server).
2. Register route mocks for `POST /query`, `GET /graph*`, `GET /files`, `GET /admin`
   before navigation (so no request races the mock registration).
3. Fill `#query-input` with a sample query string, click the submit button inside
   `[data-testid="query-form"]`.
4. Assert `[data-testid="query-answer"]` becomes visible and contains the mocked answer
   text -- "submit a query, see an answer."
5. Click the citation link (or directly navigate via the nav bar) to `/graph`; fill
   `[data-testid="graph-path-input"]` with a path and submit `[data-testid="graph-form"]`.
6. Assert `[data-testid="graph-result"]` is visible and at least one
   `[data-testid="graph-neighbor"]` row renders -- "navigate the graph view...without
   errors."
7. Navigate to `/files` via the nav bar.
8. Assert `[data-testid="admin-result"]` and `[data-testid="files-result"]` are both
   visible with at least one `[data-testid="files-catalog-row"]` -- "navigate...files
   views without errors."
9. Throughout the whole script, attach a `page.on("console", ...)` / `page.on("pageerror",
   ...)` listener from the very first navigation and assert, at the end of the test, that
   no `error`-level console messages or uncaught page errors were recorded -- "without
   errors" as a first-class, explicit assertion rather than an implicit side effect of not
   crashing.

## Wiring

- Add `@playwright/test` to `ui/package.json` devDependencies.
- Add `ui/playwright.config.ts`: `testDir: "./e2e"`, `webServer` block running
  `npm run dev` against `http://localhost:5173` (Vite's default port) with
  `reuseExistingServer: !process.env.CI`, single `chromium` project (keeps the browser
  matrix minimal for a smoke test; other browsers are future work).
- Add `"test:e2e": "playwright test"` npm script to `ui/package.json`.
- Add a `.gitignore` entries for `ui/playwright-report/` and `ui/test-results/` (Playwright's
  default output dirs) if not already covered.
- Document briefly in `ui/README.md`: what the e2e test covers, how to run it locally
  (`npm run test:e2e`, requires `npx playwright install chromium` once), and that the
  backend is mocked via route interception (no real Go server required).

## Explicit non-goals (disclosed, left as future work)

- Wiring `test:e2e` into an actual CI workflow file (no CI config found/touched in this
  repo as part of this subtask's impacted modules).
- Testing against a real running Go backend (out of scope per the acceptance criteria's
  own "(or mocked)" allowance, and no such backend exists yet for `/graph`/`/files`/`/admin`).
- Cross-browser matrix (firefox/webkit) -- chromium only, sufficient for a "basic" smoke
  test per the subtask's own title.
