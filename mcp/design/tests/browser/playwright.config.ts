import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: ".",
  testMatch: "inspection.spec.ts",
  workers: 1,
  outputDir: "../../.test-results",
  expect: { timeout: 15000 },
  use: {
    baseURL: "http://127.0.0.1:18185",
    viewport: { width: 1600, height: 1050 },
    launchOptions: {
      channel: "chrome",
      args: [
        "--enable-webgl",
        "--use-gl=angle",
        "--use-angle=swiftshader",
        "--enable-unsafe-swiftshader",
      ],
    },
  },
  webServer: {
    cwd: process.cwd(),
    command: "bun run mcp/design/tests/browser/server.ts",
    url: "http://127.0.0.1:18185",
    reuseExistingServer: true,
  },
  reporter: "list",
});
