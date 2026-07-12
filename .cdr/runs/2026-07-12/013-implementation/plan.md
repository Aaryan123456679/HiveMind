# Plan — Subtask 6.1.2

1. **QueryView.tsx — state & form**
   - `useState` for: `queryText` (input value), `status` (`"idle" | "loading" | "error" |
     "success"`), `result` (`{ answer: string; citations: string[] } | null`), `errorMessage`
     (`string | null`).
   - A `<form data-testid="query-form" onSubmit={...}>` with a labelled `<input>`
     (`aria-label="Query"`, controlled by `queryText`) and a submit `<button>` ("Submit" or
     "Ask"). Prevent default, ignore empty/whitespace-only submissions client-side.

2. **QueryView.tsx — submit handler**
   - On submit: set status to `loading`, clear previous error/result, `POST` to `/query` via
     `fetch` with `Content-Type: application/json` and body `{ query: queryText }` (no
     `history` yet — no conversation state exists in this subtask's scope).
   - On non-OK response: read response text, set status `error` with that message.
   - On OK response: `await res.json()` typed as `{ answer: string; citations: string[] }`, set
     `result` and status `success`.
   - On thrown network error: catch, set status `error` with `err.message`.

3. **QueryView.tsx — rendering**
   - Always render the form.
   - While `status === "loading"`: render a `data-testid="query-loading"` indicator (e.g. "Loading...").
   - While `status === "error"`: render `data-testid="query-error"` with `errorMessage`.
   - While `status === "success"` and `result` set: render
     - `data-testid="query-answer"` paragraph with `result.answer`.
     - A `data-testid="query-citations"` `<ul>` listing each citation as an `<li>` containing a
       `react-router-dom` `<Link to={`/files?path=${encodeURIComponent(citation)}`}>{citation}</Link>`
       (clickable, visible file path text — satisfies acceptance criteria). Use `citation` as
       React key (paths are unique per response).
     - If `result.citations` is empty, render a `data-testid="query-no-citations"` note instead
       of an empty list (defensive UX, not required by acceptance criteria but low-cost).

4. **QueryView.tsx — cleanup**
   - Remove the `useEffect`/`fetchQueryResult` import from `mockClient` entirely (no longer
     needed — querying is user-triggered, not on-mount).
   - Keep the `<h1>Query</h1>` heading (App.test.tsx and the router asserts on it).

5. **App.test.tsx — minimal dependent update**
   - Change the `/query` row in the `routes` array: replace `statusTestId: "query-status"` with
     an entry indicating the query form should be asserted instead (e.g. add a
     `formTestId`/reuse `statusTestId` field name but point it at `"query-form"`), OR special
     case query row's assertion. Simplest: keep the array shape, rename the query row's
     `statusTestId` value to `"query-form"` since `QueryView` always renders
     `data-testid="query-form"` synchronously (works fine with `findByTestId`, which resolves
     immediately for already-present elements). No other route's assertions change.
   - Leave the `vi.mock("./api/mockClient", ...)` block as-is (other views still use it); the
     unused `fetchQueryResult` mock entry is harmless.

6. **QueryView.test.tsx — new component test**
   - `vi.stubGlobal("fetch", vi.fn())` (or assign `global.fetch = vi.fn()`) per test, restore
     via `vi.unstubAllGlobals()`/`afterEach`.
   - Wrap render in `<MemoryRouter>` (QueryView uses `Link`, which requires a Router context).
   - Test 1 ("submits query and renders answer + citations"): mock `fetch` to resolve with
     `{ ok: true, json: async () => ({ answer: "Synthesized answer text", citations:
     ["docs/foo.md", "docs/bar.md"] }) }`. Render `<QueryView />`, type into the query input,
     submit the form, assert `fetch` was called once with `"/query"` and a JSON body containing
     the typed query text, then `findByTestId("query-answer")` has the answer text and
     `findByTestId("query-citations")` contains both `docs/foo.md` and `docs/bar.md` as link
     text (`getByRole("link", { name: "docs/foo.md" })`).
   - Test 2 ("renders an error message when the /query call fails"): mock `fetch` to resolve
     with `{ ok: false, text: async () => "query must not be empty" }` (mirrors
     `api/routes/query.go`'s 400 body), assert `query-error` renders that message.
   - Test 3 ("does not submit an empty query"): submit with empty input, assert `fetch` is not
     called.

7. **Self-consistency**: run `npm run build` and `npm test` inside `ui/`; both must pass before
   committing.
