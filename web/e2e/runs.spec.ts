import { expect, test, type Page } from '@playwright/test'
import { planned, stubApprovals, stubFreezes, stubPlanRoutes } from './stubs'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

// A run flow in a real browser: the real orchestrator, the real runner and the run engine's fake steps.
// The fake steps take 2 s each and a "slow" version 30 s (fixed in cmd/runner), so the timeouts here are long.

const baseURL = process.env.YUMDRAKE_E2E_BASE_URL ?? ''
const email = process.env.YUMDRAKE_E2E_EMAIL ?? ''
const password = process.env.YUMDRAKE_E2E_PASSWORD ?? ''
const jobTitle = 'Backend and microservices'

/** Screenshots are written only when YUMDRAKE_SHOTS_DIR is set, at two widths. */
async function shots(page: Page, name: string) {
  const dir = process.env.YUMDRAKE_SHOTS_DIR
  if (!dir) return
  mkdirSync(dir, { recursive: true })
  for (const width of [1360, 380]) {
    await page.setViewportSize({ width, height: 900 })
    await page.screenshot({ path: resolve(dir, `${name}-${width}.png`), fullPage: true, animations: 'disabled' })
  }
  await page.setViewportSize({ width: 1360, height: 900 })
}

const problems: string[] = []

test.afterEach(async ({ browserName }, testInfo) => {
  void browserName
  if (testInfo.status !== testInfo.expectedStatus && problems.length > 0) {
    await testInfo.attach('browser-problems', { body: problems.join('\n'), contentType: 'text/plain' })
  }
  problems.length = 0
})

/** From the job page's Run tab: pick the versions, plan, and land on the run page. Returns the run's number. */
async function planRun(page: Page, backendsVersion: string) {
  await page.getByRole('tab', { name: 'Run' }).click()
  await expect(page.getByRole('heading', { name: 'Environment', level: 2 })).toBeVisible()
  await page.getByLabel('Backend services version').selectOption(backendsVersion)
  planned.backends = backendsVersion
  await expect(page.getByText('1 service selected')).toBeVisible()
  return page.getByRole('button', { name: 'Plan this run' })
}

test('a run with a failing service is planned, started, watched and read, and a slow one is cancelled', async ({ page }) => {
  test.setTimeout(300_000)
  expect(baseURL, 'the global setup must provide the orchestrator address').not.toBe('')

  page.on('console', (message) => {
    if (message.type() === 'error' && !message.text().includes('status of 401')) problems.push(`console error: ${message.text()}`)
  })
  page.on('response', (response) => {
    if (response.status() >= 400 && !(response.status() === 401 && new URL(response.url()).pathname === '/api/v1/me')) problems.push(`${response.status()} from ${new URL(response.url()).pathname}`)
  })
  page.on('pageerror', (error) => problems.push(`page error: ${error.message}`))
  await stubApprovals(page)
  await stubFreezes(page)
  await stubPlanRoutes(page)

  // Sign in (the password is set natively so no step title shows it) and open the job.
  await page.goto(`${baseURL}/sign-in`)
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').evaluate((element, secret) => {
    const input = element as HTMLInputElement
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, secret)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }, password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).not.toHaveURL(/sign-in/)
  await page.goto(`${baseURL}/jobs`)
  await expect(page.getByRole('heading', { name: 'Jobs', level: 1 })).toBeVisible()
  await page.getByRole('table', { name: 'Jobs' }).getByRole('link', { name: jobTitle }).click()
  await expect(page.getByRole('heading', { name: jobTitle, level: 1 })).toBeVisible()
  const jobUrl = page.url()

  // ---- First run: backend-api at a failing version (orders is not in hetzner-01, so it cannot be selected) ----
  let planButton = await planRun(page, '1.0.0-fail')
  await expect(page.getByText('Backend services: 1 service at 1.0.0-fail')).toBeVisible()
  await shots(page, 'run-tab')
  await planButton.click()

  await expect(page).toHaveURL(/\/runs\/\d+$/)
  const failingRunUrl = page.url()
  await expect(page.getByTestId('run-lead')).toHaveText('Review the plan, then start')
  await shots(page, 'run-planned')
  await page.getByRole('button', { name: 'Start', exact: true }).click()

  // The state changes by itself, without a reload: planned -> running -> a final state.
  await expect(page.getByTestId('run-lead')).not.toHaveText('Review the plan, then start')
  // Lines arrive while the page stays open: a marker set on the window survives only if nothing reloaded.
  await page.evaluate(() => { (window as unknown as { __noReload: boolean }).__noReload = true })
  const consoleRegion = page.getByRole('region', { name: 'Console' })
  const logLines = consoleRegion.getByRole('log', { name: 'Log lines' })
  await expect(consoleRegion.getByText(/[1-9]\d* of [1-9]\d* lines/)).toBeVisible({ timeout: 60_000 })
  await expect(logLines).not.toContainText('No log lines yet')
  const tree = page.getByRole('navigation', { name: 'Steps and services' })
  await expect(tree.getByRole('button', { name: /backend-api/ }).first()).toBeVisible()
  await shots(page, 'run-running')

  await expect(page.getByRole('heading', { name: /^Run #\d+/ }).getByText('Partly succeeded')).toBeVisible({ timeout: 90_000 })
  const failed = page.getByRole('region', { name: 'What failed' })
  await expect(failed).toContainText('backend-api')
  await expect(failed).toContainText('the new tasks did not become healthy')
  await shots(page, 'run-failed')
  expect(await page.evaluate(() => (window as unknown as { __noReload?: boolean }).__noReload), 'the page was reloaded').toBe(true)

  // A page laid out wide and then narrowed still paints the console's lines.
  await page.setViewportSize({ width: 380, height: 900 })
  await logLines.scrollIntoViewIfNeeded()
  await expect(logLines.locator('.con-ln').first()).toBeInViewport()
  await page.setViewportSize({ width: 1360, height: 900 })

  // The whole run shows run-level lines; one service's log does not, and they return with the whole run.
  await expect(logLines).toContainText('Run created by')
  await failed.getByRole('button', { name: 'Show its log' }).click()
  await expect(page).toHaveURL(/service=backend-api/)
  await expect(consoleRegion.getByText(/[1-9]\d* error lines?/)).toBeVisible()
  await expect(logLines).toContainText('tasks started but the target group reports unhealthy')
  await expect(logLines).not.toContainText('Run created by')
  await tree.getByRole('button', { name: /Whole run/ }).click()
  await expect(logLines).toContainText('Run created by')

  // ---- Second run: a slow version, cancelled from the run page ----
  await page.goto(jobUrl)
  planButton = await planRun(page, '1.0.0-slow')
  await planButton.click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  const slowRunUrl = page.url()
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible({ timeout: 30_000 })
  await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Cancel this run?' })
  await expect(dialog).toContainText('Steps that have not started will not run. Services already rolling will finish. Nothing is rolled back.')
  await dialog.getByRole('button', { name: 'Cancel run' }).click()
  const title = page.getByRole('heading', { name: /^Run #\d+/ })
  await expect(title.getByText(/^(Cancelling|Cancelled)$/)).toBeVisible()
  // A slow roll finishes on its own (30 s) after the cancel request, then the run is cancelled.
  await expect(title.getByText('Cancelled', { exact: true })).toBeVisible({ timeout: 90_000 })

  // ---- The Runs list shows both ----
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Runs' }).click()
  await expect(page.getByRole('heading', { name: 'Runs', level: 1 })).toBeVisible()
  const runsTable = page.getByRole('table')
  const runNumber = (url: string) => url.split('/').pop() ?? ''
  await expect(runsTable.getByRole('link', { name: `#${runNumber(failingRunUrl)}` })).toBeVisible()
  await expect(runsTable.getByRole('link', { name: `#${runNumber(slowRunUrl)}` })).toBeVisible()
  await expect(runsTable).toContainText('Partly succeeded')
  await expect(runsTable).toContainText('Cancelled')
  await shots(page, 'runs-list')

  expect(problems, 'the browser reported errors').toEqual([])
})
