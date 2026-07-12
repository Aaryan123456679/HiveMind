# ui/

React dashboard for HiveMind: document upload, query with a `k` (topic count) slider,
knowledge-graph visualization, and benchmark result charts.

## Status

Subtask 6.1.1 (issue #30) scaffolded the app shell: Vite + React + TypeScript, with
react-router-dom wiring five routes that mirror `api/`'s HTTP gateway routes
(`/ingest /query /graph /files /admin`, see `docs/HLD.md` section 3.1). Each route currently
renders a minimal placeholder view; real feature content lands in later subtasks:

- 6.1.2 — `/query`: submit query, display synthesized answer + citations.
- 6.1.3 — `/graph`: visualize topic adjacency/traversal for a selected file.
- 6.1.4 — `/files` + `/admin`: catalog browsing, ingestion status, corpus stats (combined
  into a single `FilesAdminView.tsx`).
- 6.1.5 — `ui/e2e/smoke.spec.ts`: end-to-end smoke test (Playwright) covering the
  query -> answer -> graph -> files navigation happy path, with the backend mocked via
  network-level route interception (no real Go handlers exist yet for `/graph`, `/files`,
  or `/admin`).

## Commands

```bash
npm install
npm run dev      # local dev server
npm run build    # tsc -b && vite build -> dist/
npm test         # vitest run (includes App.test.tsx's router test)

npx playwright install chromium   # one-time browser binary download
npm run test:e2e                  # playwright test -- launches a real browser against
                                   # the Vite dev server (auto-started), backend mocked
                                   # via page.route()
```
