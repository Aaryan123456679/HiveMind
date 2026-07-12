# Requirement — Subtask 6.1.1 (GitHub issue #30, milestone #8 "Phase 6")

Source: `gh issue view 30` (title "[6] React dashboard (ui/)", milestone "Phase 6: Demo +
deployment + load tests", impacted modules `ui/`).

## Subtask 6.1.1 text (verbatim from issue body)

> **6.1.1 — Dashboard scaffold + routes mirroring api/'s /ingest /query /graph /files /admin**
> - Acceptance criteria: A React app scaffold exists with routed pages/views corresponding to
>   each of the five api/ gateway routes.
> - Test spec: UI build succeeds (`npm run build`) and a router test asserts all five routes
>   render without error using mocked API responses.
> - Impacted modules: `ui/src/App.tsx, ui/src/routes/`

"Each subtask above is sized to exactly one commit" (issue #30 footer) — this run produces
exactly one local commit for 6.1.1 only. Subtasks 6.1.2-6.1.5 (query/graph/files-admin real
content, e2e smoke test) are explicitly out of scope for this run; 6.1.1 is scaffold/router
only, per the task instructions ("Keep each route component a minimal placeholder for now").

## Acceptance criteria (this run's Definition of Done)

1. `npm run build` succeeds in `ui/`.
2. A router test asserts all five routes (`/ingest`, `/query`, `/graph`, `/files`, `/admin`)
   render without error, using **mocked** API responses (no live network calls — consistent
   with api/'s own disclosed gap that `/query` is not yet wired to a live pipeline; see
   architecture-discovery.md).

## Explicit scope boundaries

- Route components are placeholders only (no real query submission, graph viz, or catalog
  listing logic — that's 6.1.2/6.1.3/6.1.4).
- `/files` and `/admin` are two separate top-level routes per the issue's route list
  (`/ingest /query /graph /files /admin` — five distinct paths), even though 6.1.4's combined
  "Files/admin view" component (`FilesAdminView.tsx`) will later serve both routes. For 6.1.1,
  both `/files` and `/admin` must independently render without error (per "all five routes
  render without error").
- No live backend calls in tests or in the shell itself; this subtask does not require an HTTP
  client at all yet (that arrives with 6.1.2's `/query` wiring).
