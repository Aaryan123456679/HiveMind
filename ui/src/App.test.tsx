import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";

// Router test for subtask 6.1.1 (GitHub issue #30): asserts all five routes mirroring api/'s
// gateway (/ingest /query /graph /files /admin) render without error, using mocked API
// responses -- per the acceptance criteria / test spec in the issue. No real network call is
// ever attempted: ../api/mockClient is mocked below. As of subtask 6.1.4, IngestView is the
// only remaining mockClient consumer -- QueryView, GraphView, and FilesAdminView all call
// `fetch` directly, so `fetch` is stubbed globally below to cover those three routes.
vi.mock("./api/mockClient", () => ({
  fetchIngestStatus: vi.fn().mockResolvedValue({ ok: true, note: "mocked ingest" }),
}));

// The /query, /graph, /files, and /admin routes' assertions target their real form/result
// testids rather than a mockClient-sourced "*-status" placeholder: subtask 6.1.2 replaced
// QueryView's on-mount mockClient placeholder with a real, user-triggered query form, 6.1.3
// did the same for GraphView, and 6.1.4 did the same for FilesAdminView, which now serves
// both /files and /admin (see FilesAdminView.tsx's header comment for the consolidation
// decision) -- all three now call `fetch` directly instead of mockClient.
const routes: Array<{ path: string; heading: string; statusTestId: string }> = [
  { path: "/ingest", heading: "Ingest", statusTestId: "ingest-status" },
  { path: "/query", heading: "Query", statusTestId: "query-form" },
  { path: "/graph", heading: "Graph", statusTestId: "graph-form" },
  { path: "/files", heading: "Files", statusTestId: "files-result" },
  { path: "/admin", heading: "Files", statusTestId: "admin-result" },
];

describe("App router", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/files") {
          return Promise.resolve({
            ok: true,
            json: async () => ({ files: [] }),
          });
        }
        if (url === "/admin") {
          return Promise.resolve({
            ok: true,
            json: async () => ({ fileCount: 0, statusCounts: {} }),
          });
        }
        return Promise.resolve({ ok: true, json: async () => ({}) });
      })
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each(routes)(
    "renders $path without error",
    async ({ path, heading, statusTestId }) => {
      render(
        <MemoryRouter initialEntries={[path]}>
          <App />
        </MemoryRouter>
      );

      expect(screen.getByRole("heading", { name: heading })).toBeInTheDocument();

      // Await the mocked API resolution so React state updates settle inside `act` before
      // the test (and render tree) is torn down.
      expect(await screen.findByTestId(statusTestId)).toBeInTheDocument();
    }
  );

  it("redirects the root path to /query", async () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <App />
      </MemoryRouter>
    );

    expect(screen.getByRole("heading", { name: "Query" })).toBeInTheDocument();
    expect(await screen.findByTestId("query-form")).toBeInTheDocument();
  });
});
