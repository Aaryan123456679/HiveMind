import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import App from "./App";

// Router test for subtask 6.1.1 (GitHub issue #30): asserts all five routes mirroring api/'s
// gateway (/ingest /query /graph /files /admin) render without error, using mocked API
// responses -- per the acceptance criteria / test spec in the issue. No real network call is
// ever attempted: ../api/mockClient is mocked below.
vi.mock("./api/mockClient", () => ({
  fetchIngestStatus: vi.fn().mockResolvedValue({ ok: true, note: "mocked ingest" }),
  fetchQueryResult: vi.fn().mockResolvedValue({ ok: true, note: "mocked query" }),
  fetchGraphNeighbors: vi.fn().mockResolvedValue({ ok: true, note: "mocked graph" }),
  fetchFilesCatalog: vi.fn().mockResolvedValue({ ok: true, note: "mocked files" }),
  fetchAdminStats: vi.fn().mockResolvedValue({ ok: true, note: "mocked admin" }),
}));

const routes: Array<{ path: string; heading: string; statusTestId: string }> = [
  { path: "/ingest", heading: "Ingest", statusTestId: "ingest-status" },
  { path: "/query", heading: "Query", statusTestId: "query-status" },
  { path: "/graph", heading: "Graph", statusTestId: "graph-status" },
  { path: "/files", heading: "Files", statusTestId: "files-status" },
  { path: "/admin", heading: "Admin", statusTestId: "admin-status" },
];

describe("App router", () => {
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
    expect(await screen.findByTestId("query-status")).toBeInTheDocument();
  });
});
