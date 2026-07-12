# Requirement — Subtask 6.1.3 (GitHub issue #30, milestone #8 "Phase 6")

Source: `gh issue view 30` ("[6] React dashboard (ui/)").

## Subtask text (verbatim from issue body)

> **6.1.3 — Graph view: visualize topic adjacency/traversal for a selected file**
> - Acceptance criteria: Selecting a file shows its graph neighbors (from /graph) as a visual
>   adjacency/traversal view.
> - Test spec: Component test mocking a /graph response, asserting the expected number of
>   neighbor nodes render.
> - Impacted modules: `ui/src/routes/GraphView.tsx, ui/src/routes/GraphView.test.tsx`

## Preconditions (already done, verified, pushed at HEAD 71cb1bb8)

- 6.1.1 — app shell + 5 routes (`ui/src/App.tsx`, `ui/src/routes/*`), PASS_WITH_COMMENTS.
- 6.1.2 — Query view real /query wiring (`ui/src/routes/QueryView.tsx` + test), PASS_WITH_COMMENTS.

## Scope for this run

1. Replace the `GraphView.tsx` placeholder (currently just calls the mock
   `fetchGraphNeighbors()` stub from `ui/src/api/mockClient.ts`) with a real component that:
   - Lets the user select/enter a file (a path), consistent with how 6.1.2's citation links
     already point at `/files?path=<path>` — so `GraphView` should read an optional `?path=`
     query param (deep-link target) and also allow manual entry, since `FilesView`
     (6.1.4, not yet built) cannot yet supply a real file-picker.
   - Fetches that file's graph neighbors from the api gateway's `/graph` route.
   - Renders the neighbors in a visual (not just JSON-dump) adjacency view — acceptance
     criteria says "visual adjacency/traversal view", not necessarily a graph-charting
     library.
2. Add `GraphView.test.tsx`: component test mocking the `/graph` fetch response and asserting
   the expected number of neighbor nodes render (mirrors QueryView.test.tsx's `vi.stubGlobal("fetch", ...)`
   convention).

## Out of scope (explicitly not this run)

- Any real Go `/graph` HTTP handler in `api/routes/` (does not exist yet — see
  architecture-discovery.md's disclosed gap, same category of gap `api/routes/query.go` had
  before subtask 6.1.2 wired it).
- FilesView / FilesAdminView real file picker (6.1.4, not yet built).
- Any third-party graph-visualization library.
