import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "tests", testMatch: process.env.TELEPHONY_TEST_SURFACE === "panel" ? "panel.browser.ts" : "softphone.browser.ts", workers: 1, timeout: 45000,
  use: {
    baseURL: `http://127.0.0.1:${process.env.TELEPHONY_HOST_PORT || 5295}`,
    headless: true, permissions: ["microphone"],
    launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream",
      ...(process.env.TELEPHONY_MIC_WAV ? [`--use-file-for-fake-audio-capture=${process.env.TELEPHONY_MIC_WAV}`] : [])] },
  },
  webServer: { command: "bun tests/host-server.ts", url: `http://127.0.0.1:${process.env.TELEPHONY_HOST_PORT || 5295}/health`, reuseExistingServer: false },
  outputDir: ".test-results",
});
