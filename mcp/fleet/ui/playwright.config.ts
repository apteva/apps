import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./browser",
  testMatch: "*.spec.ts",
  timeout: 15000,
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:4178",
    headless: true,
    launchOptions:
      process.platform === "darwin"
        ? {
            executablePath:
              "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
          }
        : {},
    screenshot: "only-on-failure",
  },
  webServer: {
    command: "bun browser/serve.ts",
    url: "http://127.0.0.1:4178",
    reuseExistingServer: false,
  },
});
