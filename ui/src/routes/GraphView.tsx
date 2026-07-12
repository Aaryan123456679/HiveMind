import { useEffect, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";

// The /graph view (subtask 6.1.3, GitHub issue #30): selecting a file shows its graph
// neighbors (from /graph) as a visual adjacency/traversal view.
//
// Wire contract -- disclosed choice
// ----------------------------------
// Unlike /query (api/routes/query.go), there is no Go HTTP handler for /graph anywhere in this
// repo yet (api/main.go's newMux only registers routes.RegisterRoutes' single "/query" route --
// see this run's architecture-discovery.md). This subtask therefore defines the contract this
// view depends on, shaped after proto/hivemind.proto's GraphNeighborsRequest/Neighbor/
// GraphNeighborsResponse (docs/LLD/graph.md's "Traversal API"), but using file *paths* instead of
// numeric fileIDs as the identifier -- every other UI-visible file identifier in this codebase
// (QueryView.tsx's citation links, "/files?path=...") is already a path string, and there is no
// Go handler to reconcile a fileID-based contract against yet:
//
//   GET /graph?path=<encodeURIComponent(path)>
//   200 -> { "path": string, "neighbors": [{ "path": string, "edgeType": string, "weight": number, "hop": number }] }
//   4xx/5xx -> plain-text error body (http.Error), same convention as /query.
//
// File selection
// ---------------
// FilesView (6.1.4) does not exist yet, so this view supports both a manual path-entry form and
// an optional `?path=` search-param deep link (matching QueryView's existing
// `/files?path=<path>` citation-link convention), so a future FilesView link can point straight
// at `/graph?path=<path>` once it exists.
//
// Visualization
// --------------
// No graph-visualization library is added (none exists in ui/package.json). Neighbors are
// rendered as a real, testable list of DOM nodes (one per neighbor) alongside an SVG connector
// column showing a line from the selected file to each neighbor -- a genuine adjacency view
// without the weight/opacity of a third-party charting library.
export interface GraphNeighbor {
  path: string;
  edgeType: string;
  weight: number;
  hop: number;
}

export interface GraphResult {
  path: string;
  neighbors: GraphNeighbor[];
}

type Status = "idle" | "loading" | "error" | "success";

export default function GraphView() {
  const [searchParams, setSearchParams] = useSearchParams();
  const initialPath = searchParams.get("path") ?? "";

  const [pathInput, setPathInput] = useState(initialPath);
  const [status, setStatus] = useState<Status>("idle");
  const [result, setResult] = useState<GraphResult | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function loadGraph(path: string) {
    setStatus("loading");
    setErrorMessage(null);
    setResult(null);

    try {
      const response = await fetch(`/graph?path=${encodeURIComponent(path)}`, {
        method: "GET",
      });

      if (!response.ok) {
        const message = await response.text();
        setErrorMessage(message || `request failed with status ${response.status}`);
        setStatus("error");
        return;
      }

      const data = (await response.json()) as GraphResult;
      setResult(data);
      setStatus("success");
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : "unknown error");
      setStatus("error");
    }
  }

  // Deep-link support: auto-fetch when the view is mounted with a ?path= search param already
  // present (e.g. navigated to from a future FilesView link), mirroring QueryView's citation
  // links that already point at "/files?path=...".
  useEffect(() => {
    const path = searchParams.get("path");
    if (path && path.trim() !== "") {
      loadGraph(path.trim());
    }
    // Only re-run when the search-param-derived path actually changes -- not on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams.get("path")]);

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const trimmed = pathInput.trim();
    if (trimmed === "") {
      return;
    }

    setSearchParams({ path: trimmed });
    loadGraph(trimmed);
  }

  return (
    <section>
      <h1>Graph</h1>
      <form data-testid="graph-form" onSubmit={handleSubmit}>
        <label htmlFor="graph-path-input">File path</label>
        <input
          id="graph-path-input"
          aria-label="File path"
          data-testid="graph-path-input"
          value={pathInput}
          onChange={(event) => setPathInput(event.target.value)}
        />
        <button type="submit" disabled={status === "loading"}>
          View graph
        </button>
      </form>

      {status === "loading" && <p data-testid="graph-loading">Loading...</p>}

      {status === "error" && errorMessage && (
        <p data-testid="graph-error" role="alert">
          {errorMessage}
        </p>
      )}

      {status === "success" && result && (
        <div data-testid="graph-result">
          <p data-testid="graph-center-path">{result.path}</p>

          {result.neighbors.length > 0 ? (
            <div data-testid="graph-neighbors">
              <svg
                width="24"
                height={result.neighbors.length * 32}
                aria-hidden="true"
                data-testid="graph-connectors"
              >
                {result.neighbors.map((_, index) => (
                  <line
                    key={index}
                    x1="0"
                    y1="0"
                    x2="24"
                    y2={index * 32 + 16}
                    stroke="currentColor"
                  />
                ))}
              </svg>
              <ul>
                {result.neighbors.map((neighbor) => (
                  <li key={`${neighbor.path}-${neighbor.hop}`} data-testid="graph-neighbor">
                    <span data-testid="graph-neighbor-path">{neighbor.path}</span>{" "}
                    <span data-testid="graph-neighbor-type">{neighbor.edgeType}</span>{" "}
                    (hop {neighbor.hop}, weight {neighbor.weight})
                  </li>
                ))}
              </ul>
            </div>
          ) : (
            <p data-testid="graph-no-neighbors">No graph neighbors found for this file.</p>
          )}
        </div>
      )}
    </section>
  );
}
