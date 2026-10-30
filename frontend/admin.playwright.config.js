import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './tests', testMatch: 'admin.spec.js',
  use: { baseURL: 'http://127.0.0.1:18110', channel: process.env.PLAYWRIGHT_CHANNEL || undefined },
  webServer: { command: 'npm run dev -- --port 18110', url: 'http://127.0.0.1:18110', reuseExistingServer: !process.env.CI },
  reporter: 'list',
});
