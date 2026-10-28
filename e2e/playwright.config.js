const path = require('node:path');
const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: '.',
  testMatch: '**/*.spec.js',
  fullyParallel: false,
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { outputFolder: path.resolve(__dirname, '../.verification/playwright-report'), open: 'never' }]],
  outputDir: path.resolve(__dirname, '../.verification/playwright-results'),
  use: {
    baseURL: process.env.EMBERDECK_E2E_BASE_URL || 'http://127.0.0.1:18081',
    browserName: 'chromium',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
  },
});
