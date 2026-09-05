import { defineConfig } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
const dir = process.env.ANALYTICS_BROWSER_DIR!;
export default defineConfig({
  testDir: "./browser",
  testMatch: "**/*.spec.ts",
  workers: 1,
  fullyParallel: false,
  timeout: 30000,
  use: {
    baseURL: readFileSync(join(dir, "url"), "utf8").trim(),
    headless: true,
    viewport: { width: 1400, height: 1000 },
    launchOptions: {
      executablePath:
        process.env.CHROME_PATH ||
        (process.platform === "darwin"
          ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
          : undefined),
    },
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  reporter: [["list"], ["json", { outputFile: join(dir, "results.json") }]],
  outputDir: join(dir, "results"),
});
