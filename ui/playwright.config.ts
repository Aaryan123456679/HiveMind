import { defineConfig, devices } from "@playwright/test";

// Playwright config for subtask 6.1.5 (GitHub issue #30): the first browser-level e2e
// tooling in ui/. Mirrors ui/vite.config.ts / ui/vitest.config.ts's existing convention of
// one dedicated config file per tool rather than merging test config into vite.config.ts.
//
// Scope: a single chromium project is enough for a "basic" smoke test per the subtask's
// own title -- cross-browser (firefox/webkit) coverage is explicitly out of scope here
// (see this run's plan.md).
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"]],
  use: {
    baseURL: "http://localhost:5173",
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  webServer: {
    command: "npm run dev -- --port 5173 --strictPort",
    url: "http://localhost:5173",
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
