import { defineConfig, devices } from '@playwright/test'

// The browser test runs against the real orchestrator binary, started by e2e/global-setup.ts
// through the webstack helper (test/e2e/webstack). It never uses the Vite dev server.
export default defineConfig({
  testDir: 'e2e',
  testMatch: '*.spec.ts',
  globalSetup: './e2e/global-setup.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  // The guard must come after the html reporter: it reads the report that reporter writes.
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }], ['./e2e/secret-guard-reporter.ts']],
  outputDir: 'test-results',
  use: {
    ...devices['Desktop Chrome'],
    viewport: { width: 1360, height: 900 },
    // Off on purpose: a trace records typed values and request bodies, which would put the sign-in
    // password into the uploaded report. The test starts its own trace after signing in.
    trace: 'off',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
