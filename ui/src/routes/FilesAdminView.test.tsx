import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import FilesAdminView from "./FilesAdminView";

// Component test for subtask 6.1.4 (GitHub issue #30): mocks /files and /admin responses,
// asserting catalog rows and stats render correctly (the issue's test spec), plus the ?path=
// deep-link highlight behavior that closes QueryView's citation dead-end (carried forward
// from 6.1.2's verification). Mocks global `fetch`, mirroring QueryView.test.tsx/
// GraphView.test.tsx's convention (FilesAdminView's real implementation calls fetch directly
// against "/files" and "/admin", same as QueryView/GraphView do against their routes).
function renderView(initialPath?: string) {
  const initialEntries = initialPath ? [`/files?path=${encodeURIComponent(initialPath)}`] : ["/files"];
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <FilesAdminView />
    </MemoryRouter>
  );
}

const filesResponse = {
  files: [
    { path: "docs/foo.md", status: "ACTIVE", sizeBytes: 1024, lastModified: "2026-07-01" },
    { path: "docs/bar.md", status: "SPLITTING", sizeBytes: 4096, lastModified: "2026-07-05" },
    { path: "docs/baz.md", status: "REDIRECT", sizeBytes: 512, lastModified: "2026-07-09" },
  ],
};

const adminResponse = {
  fileCount: 3,
  statusCounts: { ACTIVE: 1, SPLITTING: 1, SPLIT: 0, REDIRECT: 1 },
};

function mockFetchImplementation() {
  return vi.fn().mockImplementation((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/files") {
      return Promise.resolve({ ok: true, json: async () => filesResponse });
    }
    if (url === "/admin") {
      return Promise.resolve({ ok: true, json: async () => adminResponse });
    }
    return Promise.resolve({ ok: false, status: 404, text: async () => "not found" });
  });
}

describe("FilesAdminView", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", mockFetchImplementation());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("renders catalog rows and corpus stats from mocked /files and /admin responses", async () => {
    renderView();

    const rows = await screen.findAllByTestId("files-catalog-row");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByTestId("files-catalog-row-path")).toHaveTextContent("docs/foo.md");
    expect(within(rows[0]).getByTestId("files-catalog-row-status")).toHaveTextContent("ACTIVE");
    expect(within(rows[1]).getByTestId("files-catalog-row-status")).toHaveTextContent("SPLITTING");
    expect(within(rows[2]).getByTestId("files-catalog-row-status")).toHaveTextContent("REDIRECT");

    expect(await screen.findByTestId("admin-file-count")).toHaveTextContent("File count: 3");
    expect(screen.getByTestId("admin-status-count-ACTIVE")).toHaveTextContent("ACTIVE: 1");
    expect(screen.getByTestId("admin-status-count-SPLITTING")).toHaveTextContent("SPLITTING: 1");
    expect(screen.getByTestId("admin-status-count-SPLIT")).toHaveTextContent("SPLIT: 0");
    expect(screen.getByTestId("admin-status-count-REDIRECT")).toHaveTextContent("REDIRECT: 1");

    expect(fetch).toHaveBeenCalledWith("/files", expect.objectContaining({ method: "GET" }));
    expect(fetch).toHaveBeenCalledWith("/admin", expect.objectContaining({ method: "GET" }));
  });

  it("highlights the catalog row matching a ?path= deep link (closing QueryView's citation dead end)", async () => {
    renderView("docs/bar.md");

    const rows = await screen.findAllByTestId("files-catalog-row");
    expect(rows).toHaveLength(3);

    const highlighted = rows.filter((row) => row.getAttribute("data-highlighted") === "true");
    expect(highlighted).toHaveLength(1);
    expect(within(highlighted[0]).getByTestId("files-catalog-row-path")).toHaveTextContent("docs/bar.md");

    const notHighlighted = rows.filter((row) => row.getAttribute("data-highlighted") === "false");
    expect(notHighlighted).toHaveLength(2);
  });

  it("does not highlight any row when no ?path= is present", async () => {
    renderView();

    const rows = await screen.findAllByTestId("files-catalog-row");
    for (const row of rows) {
      expect(row).toHaveAttribute("data-highlighted", "false");
    }
  });

  it("renders an error state when /files fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/files") {
          return Promise.resolve({ ok: false, status: 500, text: async () => "catalog unavailable" });
        }
        return Promise.resolve({ ok: true, json: async () => adminResponse });
      })
    );

    renderView();

    expect(await screen.findByTestId("files-error")).toHaveTextContent("catalog unavailable");
    expect(screen.queryByTestId("files-result")).not.toBeInTheDocument();
  });

  it("renders an error state when /admin fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/admin") {
          return Promise.resolve({ ok: false, status: 500, text: async () => "stats unavailable" });
        }
        return Promise.resolve({ ok: true, json: async () => filesResponse });
      })
    );

    renderView();

    expect(await screen.findByTestId("admin-error")).toHaveTextContent("stats unavailable");
    expect(screen.queryByTestId("admin-result")).not.toBeInTheDocument();
  });

  it("renders an empty-catalog state when /files returns no entries", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/files") {
          return Promise.resolve({ ok: true, json: async () => ({ files: [] }) });
        }
        return Promise.resolve({ ok: true, json: async () => ({ fileCount: 0, statusCounts: {} }) });
      })
    );

    renderView();

    expect(await screen.findByTestId("files-no-entries")).toBeInTheDocument();
    expect(screen.queryAllByTestId("files-catalog-row")).toHaveLength(0);
  });
});
