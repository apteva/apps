import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: ".", testMatch: "*.browser.ts", workers: 1, timeout: 30000,
  use: { baseURL: "http://127.0.0.1:5397", viewport: { width: 1400, height: 1060 }, headless: true, permissions: ["microphone"],
    launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream"] } },
  webServer: { command: "bun server.ts", url: "http://127.0.0.1:5397/health", reuseExistingServer: true },
  outputDir: "../.test-results/example",
});
