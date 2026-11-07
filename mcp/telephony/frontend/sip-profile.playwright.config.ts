import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'tests',testMatch:'sip-profile.browser.ts',workers:1,use:{baseURL:'http://127.0.0.1:5411',headless:true},webServer:{command:'bun tests/sip-profile-server.ts',url:'http://127.0.0.1:5411',reuseExistingServer:false},outputDir:'.test-results/sip-profile'});
