# Requirement -- Subtask 6.1.5 (GitHub issue #30, milestone #8 "Phase 6: Demo + deployment + load tests")

Source: `gh issue view 30` (issue #30, "[6] React dashboard (ui/)").

## Subtask 6.1.5 -- Basic end-to-end UI smoke test

This is the fifth and final subtask under issue #30. Subtasks 6.1.1 (app shell + routes),
6.1.2 (Query view), 6.1.3 (Graph view), and 6.1.4 (Files/Admin view) are implemented,
verified, and on `main` as of commit `3eefaa7`.

**Acceptance criteria** (verbatim from issue body):
> A smoke test loads the app against a running (or mocked) backend and confirms the
> happy path: submit a query, see an answer, navigate to graph and files views without
> errors.

**Test spec** (verbatim from issue body):
> An e2e test (e.g. Playwright/Cypress) covering the query -> answer -> graph -> files
> navigation happy path.

**Impacted modules** (verbatim from issue body): `ui/e2e/smoke.spec.ts`

## Decomposition of the acceptance criteria into checkable clauses

1. The app loads in a real browser context ("loads the app").
2. Backend is either running or mocked -- since no real backend is stood up in this repo
   yet (no Go HTTP handlers exist for `/graph`, `/files`, `/admin`; only `/query` is wired
   per `api/main.go`), the backend must be mocked at the network layer for this test to be
   hermetic and CI-runnable.
3. Happy path, in order:
   a. Submit a query (fill `#query-input`, click submit on `[data-testid="query-form"]`).
   b. See an answer render (`[data-testid="query-answer"]` visible with expected text).
   c. Navigate to the graph view and confirm it loads (visit `/graph`, submit a path,
      confirm `[data-testid="graph-result"]` / neighbor rows render).
   d. Navigate to the files view and confirm it loads (visit `/files`, confirm
      `[data-testid="files-result"]` catalog rows and `[data-testid="admin-result"]` stats
      render).
4. No errors during the run -- no uncaught browser exceptions / console errors during the
   entire scripted run.
