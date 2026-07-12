import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import QueryView from "./QueryView";

// Component test for subtask 6.1.2 (GitHub issue #30): asserts submitting a query calls the
// /query API and renders the returned synthesized answer plus cited file paths, per the
// acceptance criteria / test spec in the issue. Mocks the global `fetch` (QueryView's real
// implementation calls fetch directly against "/query" -- see architecture-discovery.md for
// why no client module exists to vi.mock here).
function renderQueryView() {
  return render(
    <MemoryRouter>
      <QueryView />
    </MemoryRouter>
  );
}

describe("QueryView", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("submits query and renders answer + citations", async () => {
    const user = userEvent.setup();
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        answer: "Synthesized answer text",
        citations: ["docs/foo.md", "docs/bar.md"],
      }),
    });

    renderQueryView();

    await user.type(screen.getByLabelText("Query"), "what is HiveMind?");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    expect(await screen.findByTestId("query-answer")).toHaveTextContent(
      "Synthesized answer text"
    );

    const citations = await screen.findByTestId("query-citations");
    expect(citations).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "docs/foo.md" })).toHaveAttribute(
      "href",
      "/files?path=docs%2Ffoo.md"
    );
    expect(screen.getByRole("link", { name: "docs/bar.md" })).toBeInTheDocument();

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      "/query",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ query: "what is HiveMind?" }),
      })
    );
  });

  it("renders an error message when the /query call fails", async () => {
    const user = userEvent.setup();
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: false,
      status: 400,
      text: async () => "query must not be empty",
    });

    renderQueryView();

    await user.type(screen.getByLabelText("Query"), "  ok  ");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    expect(await screen.findByTestId("query-error")).toHaveTextContent(
      "query must not be empty"
    );
    expect(screen.queryByTestId("query-answer")).not.toBeInTheDocument();
  });

  it("does not submit an empty query", async () => {
    const user = userEvent.setup();
    renderQueryView();

    await user.click(screen.getByRole("button", { name: "Submit" }));

    expect(fetch).not.toHaveBeenCalled();
  });
});
