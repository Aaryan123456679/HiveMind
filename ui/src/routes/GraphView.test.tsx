import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import GraphView from "./GraphView";

// Component test for subtask 6.1.3 (GitHub issue #30): asserts selecting a file shows its graph
// neighbors (from /graph) as a visual adjacency/traversal view, and specifically that the
// expected number of neighbor nodes render (the issue's test spec). Mocks the global `fetch`,
// mirroring QueryView.test.tsx's convention (GraphView's real implementation calls fetch
// directly against "/graph", same as QueryView does against "/query" -- see
// architecture-discovery.md for why no client module exists to vi.mock here).
function renderGraphView(initialPath?: string) {
  const initialEntries = initialPath
    ? [`/graph?path=${encodeURIComponent(initialPath)}`]
    : ["/graph"];
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <GraphView />
    </MemoryRouter>
  );
}

describe("GraphView", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("renders the expected number of neighbor nodes after selecting a file", async () => {
    const user = userEvent.setup();
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "docs/foo.md",
        neighbors: [
          { path: "docs/bar.md", edgeType: "ENTITY_COOCCUR", weight: 3, hop: 1 },
          { path: "docs/baz.md", edgeType: "LLM_ASSERTED", weight: 1, hop: 1 },
          { path: "docs/qux.md", edgeType: "SPLIT_SIBLING", weight: 0, hop: 2 },
        ],
      }),
    });

    renderGraphView();

    await user.type(screen.getByLabelText("File path"), "docs/foo.md");
    await user.click(screen.getByRole("button", { name: "View graph" }));

    expect(await screen.findByTestId("graph-center-path")).toHaveTextContent("docs/foo.md");

    const neighborNodes = await screen.findAllByTestId("graph-neighbor");
    expect(neighborNodes).toHaveLength(3);
    expect(neighborNodes[0]).toHaveTextContent("docs/bar.md");
    expect(neighborNodes[0]).toHaveTextContent("ENTITY_COOCCUR");
    expect(neighborNodes[0]).toHaveTextContent("hop 1");

    expect(fetch).toHaveBeenCalledWith(
      "/graph?path=docs%2Ffoo.md",
      expect.objectContaining({ method: "GET" })
    );
  });

  it("auto-fetches via a ?path= deep link without requiring form submission", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "docs/deep-linked.md",
        neighbors: [{ path: "docs/n1.md", edgeType: "ENTITY_COOCCUR", weight: 2, hop: 1 }],
      }),
    });

    renderGraphView("docs/deep-linked.md");

    expect(await screen.findByTestId("graph-center-path")).toHaveTextContent(
      "docs/deep-linked.md"
    );
    expect(await screen.findAllByTestId("graph-neighbor")).toHaveLength(1);
    expect(fetch).toHaveBeenCalledWith(
      "/graph?path=docs%2Fdeep-linked.md",
      expect.objectContaining({ method: "GET" })
    );
  });

  it("renders an empty state when there are no neighbors", async () => {
    const user = userEvent.setup();
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: true,
      json: async () => ({ path: "docs/isolated.md", neighbors: [] }),
    });

    renderGraphView();

    await user.type(screen.getByLabelText("File path"), "docs/isolated.md");
    await user.click(screen.getByRole("button", { name: "View graph" }));

    expect(await screen.findByTestId("graph-no-neighbors")).toBeInTheDocument();
    expect(screen.queryAllByTestId("graph-neighbor")).toHaveLength(0);
  });

  it("renders an error message when the /graph call fails", async () => {
    const user = userEvent.setup();
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: false,
      status: 404,
      text: async () => "file not found",
    });

    renderGraphView();

    await user.type(screen.getByLabelText("File path"), "docs/missing.md");
    await user.click(screen.getByRole("button", { name: "View graph" }));

    expect(await screen.findByTestId("graph-error")).toHaveTextContent("file not found");
    expect(screen.queryByTestId("graph-result")).not.toBeInTheDocument();
  });

  it("does not submit an empty path", async () => {
    const user = userEvent.setup();
    renderGraphView();

    await user.click(screen.getByRole("button", { name: "View graph" }));

    expect(fetch).not.toHaveBeenCalled();
  });
});
