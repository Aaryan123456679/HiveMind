# Requirement: subtask 6.1.4 (GitHub issue #30)

Source: `gh issue view 30` (milestone #8 "Phase 6: Demo + deployment + load tests").

Issue #30 body, subtask 6.1.4:

> **6.1.4 — Files/admin view: browse catalog, ingestion status, corpus stats**
> - Acceptance criteria: the files/admin view lists catalog entries + status, and shows
>   basic corpus-level stats (file count, ingestion status).
> - Test spec: component test mocking `/files` and `/admin` responses, asserting catalog
>   rows and stats render correctly.
> - Impacted modules: `ui/src/routes/FilesAdminView.tsx`, `ui/src/routes/FilesAdminView.test.tsx`

Prior subtasks (6.1.1 app shell, 6.1.2 Query view, 6.1.3 Graph view) are implemented,
verified, and pushed at commit `7bb8af4677ba20181bd3e4dc75236e1d739e5669` (HEAD of main at
start of this run).

## Carried-forward context from prior verifications (non-blocking, flagged for this subtask)

1. 6.1.1's verifier: `/files` and `/admin` currently exist as two separate placeholder
   components (`ui/src/routes/FilesView.tsx`, `ui/src/routes/AdminView.tsx`) rather than the
   issue's named `FilesAdminView.tsx` -- "consolidation expected in 6.1.4."
2. 6.1.2's verifier: `QueryView.tsx` links citations to `/files?path=<encoded citation>`, but
   the placeholder `FilesView.tsx` never reads that `path` query param -- a "functional dead
   end" this subtask must close by having the Files view surface/highlight the referenced
   file using that `path` param.

## Acceptance criteria (restated, concrete)

- AC1: The files/admin view lists catalog entries (per-entry: path + ingestion/lifecycle
  status) fetched from `/files`.
- AC2: The view shows corpus-level stats (file count, ingestion status summary) fetched from
  `/admin`.
- AC3 (test spec): component test mocks `/files` and `/admin` responses; asserts catalog rows
  and stats render correctly.
- AC4 (carried forward, this subtask's responsibility per issue framing + verifier notes):
  reading a `?path=` search param (consistent with GraphView's 6.1.3 deep-link convention)
  highlights/scrolls to the catalog row matching that path, so QueryView's existing citation
  links (`/files?path=<path>`) become functional instead of a dead end.
