// Placeholder API client seam for the ui/ dashboard shell (subtask 6.1.1, GitHub issue #30).
//
// api/'s Go HTTP gateway (see api/routes/query.go, api/main.go) exposes /ingest /query /graph
// /files /admin, but as of this subtask /query is only a structurally-reachable stand-in (see
// api/routes/query.go's disclosed "Real-wiring gap" -- it is not yet wired to a live end-to-end
// pipeline), and /ingest /graph /files /admin have no Go handlers at all yet. Per this
// subtask's test spec ("router test asserts all five routes render without error using mocked
// API responses"), the route shell therefore does not perform any live network call -- each
// view below imports one stub function from here so later subtasks (6.1.2 QueryView, 6.1.3
// GraphView, 6.1.4 FilesAdminView) have a stable seam to replace with real fetch calls, and so
// this subtask's router test has something concrete to `vi.mock`.
//
// Every function here is an intentionally minimal placeholder: it returns a resolved Promise
// with a trivial payload and does not touch `fetch`/XHR. Real request/response shapes will be
// filled in by the subtask that builds each view's real functionality.

export interface MockStatus {
  ok: true;
  note: string;
}

export async function fetchIngestStatus(): Promise<MockStatus> {
  return { ok: true, note: "ingest API not yet wired (subtask 6.1.2+)" };
}

export async function fetchQueryResult(): Promise<MockStatus> {
  return { ok: true, note: "query API not yet wired (subtask 6.1.2)" };
}

export async function fetchGraphNeighbors(): Promise<MockStatus> {
  return { ok: true, note: "graph API not yet wired (subtask 6.1.3)" };
}

export async function fetchFilesCatalog(): Promise<MockStatus> {
  return { ok: true, note: "files API not yet wired (subtask 6.1.4)" };
}

export async function fetchAdminStats(): Promise<MockStatus> {
  return { ok: true, note: "admin API not yet wired (subtask 6.1.4)" };
}
