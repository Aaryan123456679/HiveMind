import { mergeConfig } from "vite";
import { defineConfig } from "vitest/config";
import viteConfig from "./vite.config";

// Separate from vite.config.ts so `tsc -b` (used by `npm run build`) doesn't need to type
// the vitest-only `test` config key against vite.config.ts's plain UserConfig type.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/setupTests.ts"],
      // Exclude ui/e2e/ (subtask 6.1.5's Playwright specs) -- those are real-browser
      // tests run via `npm run test:e2e` (playwright test), not jsdom component tests.
      exclude: ["**/node_modules/**", "**/e2e/**"],
    },
  })
);
