import { defineConfig } from "@playwright/test";
import { resolve } from "node:path";
export default defineConfig({
 testDir: "./ui/tests", testMatch: "*.browser.spec.ts", workers: 1, timeout: 30000,
 outputDir: process.env.TESTS_WIDGET_ARTIFACT_DIR || "/tmp/tests-widget-browser-results",
 use: { baseURL: "http://127.0.0.1:4337", headless: true, launchOptions: process.env.CHROME_BIN ? { executablePath: process.env.CHROME_BIN } : undefined },
 webServer: { command: "bun run mcp/tests/ui/tests/host.ts", cwd: resolve(__dirname, "../.."), url: "http://127.0.0.1:4337", timeout: 180000, reuseExistingServer: false },
});
