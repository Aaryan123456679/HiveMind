# Requirement — Subtask 6.1.2 (GitHub issue #30, milestone #8 "Phase 6")

Source: `gh issue view 30` (issue #30, "[6] React dashboard (ui/)").

## Subtask 6.1.2 — Query view: submit query, display synthesized answer + cited file paths

- **Acceptance criteria**: Submitting a query calls the `/query` API and renders the returned
  answer with clickable/visible cited file paths.
- **Test spec**: Component test (React Testing Library) mocking the `/query` response,
  asserting the answer text and citation list render correctly.
- **Impacted modules**: `ui/src/routes/QueryView.tsx`, `ui/src/routes/QueryView.test.tsx`.

## Context

- Subtask 6.1.1 (app shell + 5 routes, all mocked-API scaffolds) is already implemented,
  verified (PASS_WITH_COMMENTS), and pushed at commit `b8c188b4e10b841a9e81edba0d8f76d5eff9ecfd`.
- `ui/src/routes/QueryView.tsx` currently a placeholder that calls
  `ui/src/api/mockClient.ts`'s `fetchQueryResult()` stub and renders a
  `data-testid="query-status"` note — explicitly documented in that file as "Real query
  submission + synthesized-answer/citation rendering is subtask 6.1.2 -- this is scaffold
  only."
- This subtask must replace that placeholder with real functionality: a text input + submit
  control, a call to the `/query` API, and rendering of the synthesized answer plus a list of
  clickable/visible cited file paths.
