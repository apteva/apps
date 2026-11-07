import {defineConfig} from "@playwright/test";
export default defineConfig({testDir:"tests",testMatch:"outbound-setup.browser.ts",workers:1,use:{baseURL:"http://127.0.0.1:5398",headless:true},webServer:{command:"bun tests/outbound-setup-server.ts",url:"http://127.0.0.1:5398",reuseExistingServer:false},outputDir:".test-results/outbound-setup"});
