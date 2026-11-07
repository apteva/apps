import {defineConfig} from "@playwright/test";
export default defineConfig({testDir:"tests",testMatch:"recording-playback.browser.ts",workers:1,use:{baseURL:"http://127.0.0.1:5399",headless:true},webServer:{command:"bun tests/recording-playback-server.ts",url:"http://127.0.0.1:5399",reuseExistingServer:false},outputDir:".test-results/recording-playback"});
