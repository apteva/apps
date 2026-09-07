import { defineConfig } from "@playwright/test";
export default defineConfig({testDir:"tests",testMatch:"*.browser.ts",workers:1,timeout:30_000,use:{baseURL:"http://127.0.0.1:5292",headless:true},webServer:{command:"bun tests/host-server.ts",url:"http://127.0.0.1:5292/health",reuseExistingServer:false},outputDir:".test-results"});
