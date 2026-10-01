import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests", timeout: 30000, fullyParallel: true,
  use: { baseURL: "http://127.0.0.1:4173", trace: "retain-on-failure" },
  webServer: { command: "bun run serve-preview.ts", url: "http://127.0.0.1:4173", reuseExistingServer: false },
  reporter: "list",
});
