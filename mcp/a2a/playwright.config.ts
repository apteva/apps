import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./ui",
  testMatch: "A2APanel.spec.ts",
  outputDir: "/private/tmp/a2a-v060-browser-results",
  reporter: "list",
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:4196",
    viewport: { width: 1440, height: 1040 },
    headless: true,
  },
  webServer: {
    command: "bun run mcp/a2a/ui/preview-server.ts",
    cwd: "../..",
    url: "http://127.0.0.1:4196",
    reuseExistingServer: true,
  },
});
