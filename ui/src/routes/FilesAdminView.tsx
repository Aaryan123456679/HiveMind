import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

// The /files and /admin views (subtask 6.1.4, GitHub issue #30): browse the catalog,
// ingestion status, and basic corpus-level stats.
//
// Consolidation decision
// -----------------------
// Issue #30 names a single impacted module, `ui/src/routes/FilesAdminView.tsx` (singular),
// and its test spec describes one component test "mocking /files and /admin responses" --
// i.e. one component that consumes both endpoints together, not two independently-fetching
// components. 6.1.1 already wired `/files` and `/admin` as two separate top-level routes in
// App.tsx; this subtask consolidates the *component* (superseding the old
// routes/FilesView.tsx and routes/AdminView.tsx placeholders) while keeping both existing
// route paths and nav links intact -- both now render this same FilesAdminView component
// instance, so nothing that already links to "/files" (e.g. QueryView.tsx's citation links)
// or "/admin" breaks.
//
// Wire contract -- disclosed choice
// ----------------------------------
// Like /graph (subtask 6.1.3), there is no Go HTTP handler for /files or /admin anywhere in
// this repo yet (api/main.go's newMux only registers routes.RegisterRoutes' single "/query"
// route -- see this run's architecture-discovery.md). This subtask therefore defines the
// contract this view depends on, reusing engine/catalog/'s CatalogRecord.status enum
// (docs/LLD/catalog.md: ACTIVE | SPLITTING | SPLIT | REDIRECT) as the per-entry
// ingestion/lifecycle status, and identifying files by path (matching QueryView's citation
// links and GraphView's ?path= convention) rather than numeric fileID:
//
//   GET /files
//   200 -> { "files": [{ "path": string, "status": "ACTIVE"|"SPLITTING"|"SPLIT"|"REDIRECT",
//                         "sizeBytes": number, "lastModified": string }] }
//
//   GET /admin
//   200 -> { "fileCount": number,
//            "statusCounts": { "ACTIVE": number, "SPLITTING": number, "SPLIT": number, "REDIRECT": number } }
//
//   4xx/5xx -> plain-text error body (http.Error), same convention as /query and /graph.
//
// ?path= deep link -- closes QueryView's citation dead-end
// ----------------------------------------------------------
// QueryView.tsx's citation links already point at "/files?path=<encodedPath>", but the old
// FilesView.tsx placeholder never read that param, so clicking a citation was a functional
// dead end. This view reads ?path= via useSearchParams and, once the catalog has loaded,
// highlights and scrolls to the matching row. /files is not parameterized by path in the
// contract above (it always returns the full catalog), so the deep link selects a row within
// the already-fetched listing rather than triggering a second, filtered fetch.
export type CatalogStatus = "ACTIVE" | "SPLITTING" | "SPLIT" | "REDIRECT";

export interface CatalogEntry {
  path: string;
  status: CatalogStatus;
  sizeBytes: number;
  lastModified: string;
}

export interface FilesResult {
  files: CatalogEntry[];
}

export interface AdminResult {
  fileCount: number;
  statusCounts: Partial<Record<CatalogStatus, number>>;
}

type Status = "loading" | "error" | "success";

const ALL_STATUSES: CatalogStatus[] = ["ACTIVE", "SPLITTING", "SPLIT", "REDIRECT"];

export default function FilesAdminView() {
  const [searchParams] = useSearchParams();
  const highlightPath = searchParams.get("path");

  const [filesStatus, setFilesStatus] = useState<Status>("loading");
  const [filesResult, setFilesResult] = useState<FilesResult | null>(null);
  const [filesError, setFilesError] = useState<string | null>(null);

  const [adminStatus, setAdminStatus] = useState<Status>("loading");
  const [adminResult, setAdminResult] = useState<AdminResult | null>(null);
  const [adminError, setAdminError] = useState<string | null>(null);

  const highlightedRowRef = useRef<HTMLLIElement | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function loadFiles() {
      setFilesStatus("loading");
      setFilesError(null);
      try {
        const response = await fetch("/files", { method: "GET" });
        if (!response.ok) {
          const message = await response.text();
          if (!cancelled) {
            setFilesError(message || `request failed with status ${response.status}`);
            setFilesStatus("error");
          }
          return;
        }
        const data = (await response.json()) as FilesResult;
        if (!cancelled) {
          setFilesResult(data);
          setFilesStatus("success");
        }
      } catch (err) {
        if (!cancelled) {
          setFilesError(err instanceof Error ? err.message : "unknown error");
          setFilesStatus("error");
        }
      }
    }

    async function loadAdmin() {
      setAdminStatus("loading");
      setAdminError(null);
      try {
        const response = await fetch("/admin", { method: "GET" });
        if (!response.ok) {
          const message = await response.text();
          if (!cancelled) {
            setAdminError(message || `request failed with status ${response.status}`);
            setAdminStatus("error");
          }
          return;
        }
        const data = (await response.json()) as AdminResult;
        if (!cancelled) {
          setAdminResult(data);
          setAdminStatus("success");
        }
      } catch (err) {
        if (!cancelled) {
          setAdminError(err instanceof Error ? err.message : "unknown error");
          setAdminStatus("error");
        }
      }
    }

    loadFiles();
    loadAdmin();

    return () => {
      cancelled = true;
    };
  }, []);

  // Deep-link support: once the catalog has loaded, scroll the row matching ?path= into view
  // (jsdom/test environments may not implement scrollIntoView, so guard it).
  useEffect(() => {
    if (highlightedRowRef.current && typeof highlightedRowRef.current.scrollIntoView === "function") {
      highlightedRowRef.current.scrollIntoView({ block: "center" });
    }
  }, [highlightPath, filesResult]);

  return (
    <section>
      <h1>Files</h1>

      <section aria-label="Corpus stats">
        <h2>Admin</h2>
        {adminStatus === "loading" && <p data-testid="admin-loading">Loading...</p>}
        {adminStatus === "error" && adminError && (
          <p data-testid="admin-error" role="alert">
            {adminError}
          </p>
        )}
        {adminStatus === "success" && adminResult && (
          <div data-testid="admin-result">
            <p data-testid="admin-file-count">File count: {adminResult.fileCount}</p>
            <ul data-testid="admin-status-counts">
              {ALL_STATUSES.map((statusName) => (
                <li key={statusName} data-testid={`admin-status-count-${statusName}`}>
                  {statusName}: {adminResult.statusCounts[statusName] ?? 0}
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>

      <section aria-label="File catalog">
        <h2>Catalog</h2>
        {filesStatus === "loading" && <p data-testid="files-loading">Loading...</p>}
        {filesStatus === "error" && filesError && (
          <p data-testid="files-error" role="alert">
            {filesError}
          </p>
        )}
        {filesStatus === "success" && filesResult && (
          <div data-testid="files-result">
            {filesResult.files.length > 0 ? (
              <ul>
                {filesResult.files.map((entry) => {
                  const isHighlighted = highlightPath !== null && entry.path === highlightPath;
                  return (
                    <li
                      key={entry.path}
                      ref={isHighlighted ? highlightedRowRef : undefined}
                      data-testid="files-catalog-row"
                      data-highlighted={isHighlighted ? "true" : "false"}
                    >
                      <span data-testid="files-catalog-row-path">{entry.path}</span>{" "}
                      <span data-testid="files-catalog-row-status">{entry.status}</span>{" "}
                      ({entry.sizeBytes} bytes, last modified {entry.lastModified}){" "}
                      <Link to={`/graph?path=${encodeURIComponent(entry.path)}`}>View graph</Link>
                    </li>
                  );
                })}
              </ul>
            ) : (
              <p data-testid="files-no-entries">No catalog entries found.</p>
            )}
          </div>
        )}
      </section>
    </section>
  );
}
