import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "tests",
  testMatch: "audio-dashboard.browser.ts",
  workers: 1,
  timeout: 30000,
  use: {
    baseURL: "http://127.0.0.1:5297",
    headless: true,
    viewport: { width: 1280, height: 900 },
  },
  webServer: {
    command: "bun tests/audio-dashboard-server.ts",
    url: "http://127.0.0.1:5297/health",
    reuseExistingServer: false,
  },
  outputDir: ".test-results/audio-dashboard",
});
