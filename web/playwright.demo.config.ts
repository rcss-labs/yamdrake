import { defineConfig, devices } from '@playwright/test'

// The browser test of the demo (scripts/demo-check.sh runs it against the demo it started). There is no global
// setup: the demo is already up at YUMDRAKE_DEMO_URL, seeded, with its public sign-ins.
export default defineConfig({
  testDir: 'e2e',
  testMatch: 'demo.spec.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 300_000,
  expect: { timeout: 15_000 },
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report-demo' }]],
  outputDir: 'test-results-demo',
  use: {
    ...devices['Desktop Chrome'],
    viewport: { width: 1360, height: 900 },
    trace: 'off',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
