# Plan: subtask 6.1.4 (Files/admin view)

1. **Create `ui/src/routes/FilesAdminView.tsx`** implementing:
   - (a) Catalog listing with ingestion status per entry: on mount, `GET /files` ->
     `{ files: [{ path, status, sizeBytes, lastModified }] }`; render one row per entry with
     `data-testid="files-catalog-row"`, showing path + status (+ size/lastModified).
   - (b) Corpus-level stats: on mount, `GET /admin` ->
     `{ fileCount, statusCounts: { ACTIVE, SPLITTING, SPLIT, REDIRECT } }`; render
     `data-testid="admin-file-count"` and a per-status count breakdown
     (`data-testid="admin-status-count-<STATUS>"`).
   - Both fetches fire in parallel on mount (`useEffect`, `Promise.all`-style, independent
     loading/error state per fetch, following QueryView/GraphView's `idle|loading|error|success`
     status-state convention and `data-testid` naming convention:
     `files-loading`/`files-error`/`files-result`, `admin-loading`/`admin-error`/`admin-result`).
   - (c) `?path=` deep-link handling: read via `useSearchParams()` (mirroring GraphView.tsx).
     On mount/whenever it changes, if a catalog row matches, mark it with a
     `data-highlighted="true"` attribute (rows otherwise carry `data-testid="files-catalog-row"`
     and `data-highlighted="false"`) and scroll it into view (`element.scrollIntoView`, guarded
     for jsdom/test environment where `scrollIntoView` may be undefined). This does not refetch `/files` -- the catalog is
     fetched once and the deep link just selects a row within it, since `/files` is not
     parameterized by path in the defined contract.
   - Each catalog row links to `/graph?path=<path>` (closes the reverse reference GraphView's
     own comment anticipates), addressing the "dependents" note in impact-analysis.json.
2. **Delete `ui/src/routes/FilesView.tsx` and `ui/src/routes/AdminView.tsx`** (superseded).
3. **Update `ui/src/App.tsx`**: import `FilesAdminView` in place of `FilesView`/`AdminView`;
   both `/files` and `/admin` `<Route>` entries render `<FilesAdminView />`; nav links and
   route paths unchanged.
4. **Update `ui/src/App.test.tsx`**: remove the now-inapplicable `fetchFilesCatalog`/
   `fetchAdminStats` mocks and `files-status`/`admin-status` assertions for the `/files` and
   `/admin` rows in the `routes` table; replace with assertions against FilesAdminView's real
   `data-testid`s after stubbing `global.fetch`, matching the precedent App.test.tsx's own
   comment documents for 6.1.2/6.1.3 (QueryView -> `query-form`, GraphView -> `graph-form`).
5. **Create `ui/src/routes/FilesAdminView.test.tsx`**: component test per issue #30's test
   spec, mocking `/files` and `/admin` fetch responses and asserting catalog rows + stats
   render correctly, plus a case asserting the `?path=` deep link highlights the correct row
   (closing QueryView's citation dead-end), plus loading/error-state coverage consistent with
   QueryView.test.tsx/GraphView.test.tsx conventions.
6. **Leave `ui/src/api/mockClient.ts` unmodified** (dead stubs left in place, same precedent
   as 6.1.2/6.1.3 -- confirmed via `git show --stat` on 71cb1bb/7bb8af4).
7. Run `npm run build` and `npm test` inside `ui/` to confirm everything is green
   (self-consistency, not verification).
