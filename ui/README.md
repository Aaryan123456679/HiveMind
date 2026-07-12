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
- 6.1.5 — end-to-end smoke test (Playwright/Cypress) across the happy path.

## Commands

```bash
npm install
npm run dev      # local dev server
npm run build    # tsc -b && vite build -> dist/
npm test         # vitest run (includes App.test.tsx's router test)
```
