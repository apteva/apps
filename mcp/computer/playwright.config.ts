import { resolve } from "node:path";
import { defineConfig } from "@playwright/test";
export default defineConfig({
  outputDir: process.env.TMPDIR ? `${process.env.TMPDIR}/computer-browser-view-results` : "/tmp/computer-browser-view-results",
  testDir: "./ui", testMatch: "browser-view.browser.spec.ts", workers: 1,
  use: { baseURL: "http://127.0.0.1:4319", headless: true,
    launchOptions: process.env.CHROME_BIN ? { executablePath: process.env.CHROME_BIN } : undefined },
  webServer: { command: "bun run mcp/computer/ui/test-fixtures/browser-view-server.ts", cwd: resolve(__dirname, "../.."), url: "http://127.0.0.1:4319", reuseExistingServer: false },
});
