import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./ui/tests",
  testMatch: "*.spec.ts",
  workers: 1,
  reporter: "list",
  use: {
    baseURL: "http://127.0.0.1:5394",
    headless: true,
    viewport: { width: 1440, height: 1100 },
  },
  webServer: {
    command: "bun run ui/tests/browser-server.ts",
    cwd: __dirname,
    url: "http://127.0.0.1:5394",
    reuseExistingServer: false,
  },
  outputDir: "../../.processes-test-results",
});
