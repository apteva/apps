import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'./ui/tests',testMatch:'*.spec.ts',fullyParallel:false,workers:1,reporter:'list',use:{baseURL:'http://127.0.0.1:5389',headless:true},webServer:{command:'bun run ui/tests/browser-server.ts',cwd:__dirname,url:'http://127.0.0.1:5389',reuseExistingServer:false},outputDir:'../../.code-test-results'});
