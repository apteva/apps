import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'tests/browser',testMatch:'*.spec.ts',workers:1,timeout:30000,use:{baseURL:'http://127.0.0.1:19180',channel:'chrome',headless:true},webServer:{command:'bun tests/browser/server.ts',url:'http://127.0.0.1:19180',reuseExistingServer:false},reporter:[['list'],['json',{outputFile:'playwright-report/results.json'}]]});
