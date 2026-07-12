# Architecture discovery: subtask 6.1.4 (Files/admin view)

## docs/HLD.md

Line 63-65: `api/` is the HTTP gateway providing routes `/ingest /query /graph /files /admin`
that fan out to the engine and agent service via gRPC. `ui/` (line 83) is annotated "not yet
built" at HLD authoring time; the dashboard is being built incrementally across 6.1.x
subtasks.

## api/routes/ -- confirmed: no Go handler for /files or /admin yet

`api/routes/query.go` is the only handler file; `api/main.go`'s `newMux` only registers
`routes.RegisterRoutes`'s single `"/query"` route. There is no Go-side contract for `/files`
or `/admin` at all -- same situation 6.1.3 found for `/graph`. This subtask therefore needs
the same "UI-side disclosed contract" treatment GraphView.tsx used: define the wire shape in
a comment, backed by the closest available engine-side LLD, and note it is not yet backed by
a real Go handler.

## docs/LLD/catalog.md -- closest available shape for a catalog entry

`engine/catalog/` (status: implemented) owns `CatalogRecord`:
```
fileID uint64        // monotonically increasing
pathHash
currentVersion
sizeBytes
status               // ACTIVE | SPLITTING | SPLIT | REDIRECT
redirectTargetIDs []
parentTopicID
lastModified
```
No LLD document specifically defines an HTTP-level `/files` or `/admin` JSON response shape
(there is no `docs/LLD/api.md` or similar). Per the same reasoning GraphView.tsx used for
`/graph`, this subtask defines the UI-facing contract using file *paths* (not numeric
`fileID`s) as the identifier, since every other UI-visible file identifier already in the
codebase (QueryView.tsx citations, GraphView.tsx's `?path=`) is a path string, and reusing
`CatalogRecord.status`'s four-value enum (`ACTIVE | SPLITTING | SPLIT | REDIRECT`) as the
per-entry ingestion/lifecycle status, since that is the only concretely-specified status
enum in the repo for a catalog entry.

Defined contract:
```
GET /files
200 -> { "files": [{ "path": string, "status": "ACTIVE"|"SPLITTING"|"SPLIT"|"REDIRECT",
                      "sizeBytes": number, "lastModified": string }] }

GET /admin
200 -> { "fileCount": number,
         "statusCounts": { "ACTIVE": number, "SPLITTING": number, "SPLIT": number, "REDIRECT": number } }

4xx/5xx -> plain-text error body (http.Error), same convention as /query and /graph.
```

## Existing placeholders

- `ui/src/routes/FilesView.tsx` -- placeholder, calls `fetchFilesCatalog()` from
  `ui/src/api/mockClient.ts` on mount, renders a single `data-testid="files-status"` note.
  Its own comment says it will be "superseded by a combined FilesAdminView.tsx" in 6.1.4.
- `ui/src/routes/AdminView.tsx` -- same pattern, `fetchAdminStats()`, `data-testid="admin-status"`.
- `ui/src/api/mockClient.ts` -- `fetchFilesCatalog`/`fetchAdminStats` are both trivial
  `Promise<MockStatus>` stubs (`{ ok: true, note: string }`), never touch `fetch`. Only
  consumer of both is `App.test.tsx`'s router test (mocks the whole module) and the two
  placeholder views themselves.
- `ui/src/App.tsx` -- registers `/files` and `/admin` as two separate top-level `<Route>`
  entries, each currently pointing at `FilesView`/`AdminView` respectively, both listed as
  separate nav links ("Files", "Admin").
- `ui/src/App.test.tsx` -- router test asserts, per route, a heading + a `statusTestId`
  (`files-status` / `admin-status`) sourced from the mocked `mockClient` module. This test
  will need updating once the placeholders are replaced with a real-fetch component (matching
  what 6.1.2/6.1.3 already did for `/query` and `/graph` -- App.test.tsx's own comment already
  documents that precedent: "6.1.2 replaced QueryView's on-mount mockClient placeholder... 6.1.3
  did the same for GraphView").

## QueryView.tsx -- citation dead-end to close

`QueryView.tsx` line 99: citations render as `<Link to={\`/files?path=${encodeURIComponent(citation)}\`}>`.
`FilesView.tsx` never reads `useSearchParams()` at all today, so this link is inert. GraphView
(6.1.3) already established the deep-link convention this subtask should mirror: read
`?path=` via `useSearchParams()`, and on mount, if present, use it to select/highlight the
matching entry (GraphView additionally *fetches* using the path since /graph is keyed by a
single file; /files returns a full catalog listing regardless of `path`, so for
FilesAdminView the `?path=` value is used to highlight/scroll to the matching row in the
already-fetched catalog, not to filter the request).

## Route/consolidation decision

Issue #30 names the impacted module singular: `ui/src/routes/FilesAdminView.tsx`,
`ui/src/routes/FilesAdminView.test.tsx` (no plural "Views"), and its test spec says a single
component test "mocking /files and /admin responses" (both, together) -- implying one
component consumes both endpoints, not two independently-fetching components. App.tsx's
existing `/files` and `/admin` are both top-level nav-visible paths from 6.1.1 (scaffold
subtask, out of this subtask's revision scope for *removal*), so the decision made here is to
**consolidate the component, not the routes**: implement one `FilesAdminView.tsx` component
that fetches both `/files` and `/admin` and renders catalog rows + corpus stats together, and
wire it to serve *both* existing `/files` and `/admin` top-level routes in `App.tsx` (same
component instance, same content, reachable at either path) -- this preserves 6.1.1's route
paths and nav links (no link rot for anything already relying on them, e.g. QueryView's
citation links to `/files?path=...`) while satisfying issue #30's singular
`FilesAdminView.tsx` naming and the "one component test mocking both responses" test spec.
`FilesView.tsx` and `AdminView.tsx` are deleted (superseded, per their own placeholder
comments) and `App.tsx` / `App.test.tsx` are updated accordingly.
