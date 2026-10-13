import { defineConfig } from "@playwright/test";
const port=Number(process.env.TELEPHONY_EXAMPLE_PORT || 5397);
if(!Number.isInteger(port)||port<1024||port>65535)throw new Error("Invalid Telephony example test port");
export default defineConfig({
  testDir: ".", testMatch: "*.browser.ts", workers: 1, timeout: 30000,
  use: { baseURL: `http://127.0.0.1:${port}`, viewport: { width: 1400, height: 1060 }, headless: true, permissions: ["microphone"],
    launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream"] } },
  webServer: { command: `PORT=${port} bun server.ts`, url: `http://127.0.0.1:${port}/health`, reuseExistingServer: false },
  outputDir: "../.test-results/example",
});
