import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "tests",
  testMatch: "call-inspector.browser.ts",
  workers: 1,
  timeout: 30000,
  use: {
    baseURL: "http://127.0.0.1:5298",
    headless: true,
    viewport: { width: 1400, height: 1000 },
  },
  webServer: {
    command: "bun tests/call-inspector-server.ts",
    url: "http://127.0.0.1:5298/health",
    reuseExistingServer: false,
  },
  outputDir: ".test-results/call-inspector",
});
