import { Navigate, NavLink, Route, Routes } from "react-router-dom";
import IngestView from "./routes/IngestView";
import QueryView from "./routes/QueryView";
import GraphView from "./routes/GraphView";
import FilesAdminView from "./routes/FilesAdminView";

// App shell + router wiring for the HiveMind dashboard (subtask 6.1.1, GitHub issue #30).
//
// Mirrors api/'s five HTTP gateway routes (docs/HLD.md section 3.1: "routes `/ingest /query
// /graph /files /admin`") one-to-one with client-side routes. IngestView remains a scaffold
// placeholder; QueryView (6.1.2), GraphView (6.1.3), and FilesAdminView (6.1.4) are now real,
// fetch-backed views. Per subtask 6.1.4's consolidation decision, /files and /admin both
// render the same FilesAdminView component instance (single combined component, two
// unchanged route paths -- see FilesAdminView.tsx's header comment).
//
// Deliberately does NOT wrap itself in <BrowserRouter> -- that's done once in main.tsx so
// App.test.tsx can instead wrap this same component in <MemoryRouter> to drive each of the
// five routes directly in the router test.
export default function App() {
  return (
    <div>
      <nav aria-label="primary">
        <NavLink to="/ingest">Ingest</NavLink>
        <NavLink to="/query">Query</NavLink>
        <NavLink to="/graph">Graph</NavLink>
        <NavLink to="/files">Files</NavLink>
        <NavLink to="/admin">Admin</NavLink>
      </nav>
      <main>
        <Routes>
          <Route path="/" element={<Navigate to="/query" replace />} />
          <Route path="/ingest" element={<IngestView />} />
          <Route path="/query" element={<QueryView />} />
          <Route path="/graph" element={<GraphView />} />
          <Route path="/files" element={<FilesAdminView />} />
          <Route path="/admin" element={<FilesAdminView />} />
        </Routes>
      </main>
    </div>
  );
}
