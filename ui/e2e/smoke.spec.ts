import { expect, test } from "@playwright/test";
import type { Page, Route } from "@playwright/test";

// Basic end-to-end UI smoke test (subtask 6.1.5, GitHub issue #30 -- final subtask under
// the issue). Acceptance criteria: "A smoke test loads the app against a running (or
// mocked) backend and confirms the happy path: submit a query, see an answer, navigate to
// graph and files views without errors."
//
// Disclosed choice -- what "mocked backend" means here
// -------------------------------------------------------
// No Go HTTP handler exists yet anywhere in this repo for /graph, /files, or /admin (only
// /query is registered, per api/main.go), and no standalone runnable backend seeded with
// test data exists in this environment for /query either. Per the acceptance criteria's own
// "(or mocked)" allowance, this test mocks all four endpoints the happy path touches at the
// network layer via Playwright's page.route() -- NOT via component-level mocking (that's
// what the existing vitest+RTL tests already do in ui/src/routes/*.test.tsx). This test
// launches a real Chromium browser against the real Vite dev server (see
// ui/playwright.config.ts's webServer block) and drives the real bundled app; only the
// fetch() responses are faked, matching the wire contracts disclosed in QueryView.tsx,
// GraphView.tsx, and FilesAdminView.tsx's own header comments.
//
// Scope: chromium only (see ui/playwright.config.ts), no real backend process, no CI
// wiring (left as explicit future work -- see this run's handoff.json).

const MOCK_ANSWER = "HiveMind ingests markdown files and answers queries with citations.";
const MOCK_CITATION = "docs/example.md";

const MOCK_GRAPH_PATH = "docs/example.md";
const MOCK_NEIGHBOR_PATH = "docs/related.md";

function registerBackendMocks(page: Page) {
  // POST /query -- QueryView.tsx's wire contract:
  //   { "query": string } -> { "answer": string, "citations": string[] }
  return Promise.all([
    page.route("**/query", async (route: Route) => {
      if (route.request().method() !== "POST") {
        return route.fallback();
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ answer: MOCK_ANSWER, citations: [MOCK_CITATION] }),
      });
    }),

    // GET /graph?path=... -- GraphView.tsx's wire contract:
    //   { "path": string, "neighbors": [{ "path", "edgeType", "weight", "hop" }] }
    page.route("**/graph?**", async (route: Route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          path: MOCK_GRAPH_PATH,
          neighbors: [{ path: MOCK_NEIGHBOR_PATH, edgeType: "REFERENCES", weight: 0.8, hop: 1 }],
        }),
      });
    }),

    // GET /files -- FilesAdminView.tsx's wire contract:
    //   { "files": [{ "path", "status", "sizeBytes", "lastModified" }] }
    page.route("**/files", async (route: Route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          files: [
            {
              path: MOCK_CITATION,
              status: "ACTIVE",
              sizeBytes: 1024,
              lastModified: "2026-07-01T00:00:00Z",
            },
          ],
        }),
      });
    }),

    // GET /admin -- FilesAdminView.tsx's wire contract:
    //   { "fileCount": number, "statusCounts": { ACTIVE, SPLITTING, SPLIT, REDIRECT } }
    page.route("**/admin", async (route: Route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          fileCount: 1,
          statusCounts: { ACTIVE: 1, SPLITTING: 0, SPLIT: 0, REDIRECT: 0 },
        }),
      });
    }),
  ]);
}

test.describe("HiveMind dashboard smoke test", () => {
  test("submit query, see answer, navigate graph and files views without errors", async ({
    page,
  }) => {
    const consoleErrors: string[] = [];
    const pageErrors: string[] = [];

    // Registered before the first navigation so no console/page error during the whole
    // happy-path run goes unnoticed -- "without errors" is asserted explicitly, not just
    // implied by the test not crashing.
    page.on("console", (msg) => {
      if (msg.type() === "error") {
        consoleErrors.push(msg.text());
      }
    });
    page.on("pageerror", (err) => {
      pageErrors.push(err.message);
    });

    await registerBackendMocks(page);

    // 1. Load the app (default route redirects "/" -> "/query").
    await page.goto("/query");
    await expect(page.getByRole("heading", { name: "Query" })).toBeVisible();

    // 2. Submit a query.
    const queryRequestPromise = page.waitForRequest(
      (req) => req.url().includes("/query") && req.method() === "POST",
    );
    await page.getByLabel("Query").fill("What does HiveMind do?");
    await page.getByTestId("query-form").getByRole("button", { name: "Submit" }).click();
    await queryRequestPromise;

    // 3. See the answer render.
    await expect(page.getByTestId("query-answer")).toBeVisible();
    await expect(page.getByTestId("query-answer")).toHaveText(MOCK_ANSWER);
    await expect(page.getByTestId("query-citations")).toContainText(MOCK_CITATION);

    // 4. Navigate to the graph view and confirm it loads.
    await page.getByRole("link", { name: "Graph" }).click();
    await expect(page.getByRole("heading", { name: "Graph" })).toBeVisible();
    await page.getByTestId("graph-path-input").fill(MOCK_GRAPH_PATH);
    await page.getByTestId("graph-form").getByRole("button", { name: "View graph" }).click();

    await expect(page.getByTestId("graph-result")).toBeVisible();
    await expect(page.getByTestId("graph-neighbor").first()).toBeVisible();
    await expect(page.getByTestId("graph-neighbor-path").first()).toHaveText(MOCK_NEIGHBOR_PATH);
    await expect(page.getByTestId("graph-error")).toHaveCount(0);

    // 5. Navigate to the files view and confirm it loads.
    await page.getByRole("link", { name: "Files" }).click();
    await expect(page.getByRole("heading", { name: "Files" })).toBeVisible();

    await expect(page.getByTestId("admin-result")).toBeVisible();
    await expect(page.getByTestId("admin-file-count")).toContainText("1");
    await expect(page.getByTestId("files-result")).toBeVisible();
    await expect(page.getByTestId("files-catalog-row").first()).toBeVisible();
    await expect(page.getByTestId("files-catalog-row-path").first()).toHaveText(MOCK_CITATION);
    await expect(page.getByTestId("files-error")).toHaveCount(0);
    await expect(page.getByTestId("admin-error")).toHaveCount(0);

    // 6. Assert no console/page errors occurred anywhere across the whole happy path.
    expect(consoleErrors, `unexpected console errors: ${consoleErrors.join("; ")}`).toEqual([]);
    expect(pageErrors, `unexpected page errors: ${pageErrors.join("; ")}`).toEqual([]);
  });
});
