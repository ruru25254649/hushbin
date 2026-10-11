import { defineConfig } from '@playwright/test';

// End-to-end tests run against a real server: `HB_URL=http://127.0.0.1:8080 npx playwright test`.
export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.HB_URL ?? 'http://127.0.0.1:8080',
    launchOptions: process.env.PW_CHROMIUM ? { executablePath: process.env.PW_CHROMIUM } : {},
    permissions: ['clipboard-read', 'clipboard-write'],
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
});
