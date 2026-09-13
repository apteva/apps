import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "browser",
  testMatch: "*.spec.ts",
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:5319",
    timezoneId: "Europe/Madrid",
    viewport: { width: 1280, height: 900 },
    headless: true,
  },
  webServer: {
    command: "bun run browser/server.ts",
    url: "http://127.0.0.1:5319",
    timeout: 60000,
    reuseExistingServer: false,
  },
  reporter: "list",
});
