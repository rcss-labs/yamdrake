import { expect, test, type Browser, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// The whole deployment flow in a real browser, against real steps on Floci: the real orchestrator, the production
// runner (copy-config and ecs-deploy, no fake step), PostgreSQL, Valkey, a fake GitHub and two seeded AWS accounts
// (dev-1 and prod-1, three ECS services each, running 1.4.1). The stack is the second one the global setup starts
// (test/e2e/webstack --aws-endpoint); runs.spec.ts keeps using the fake steps, because it cancels a deliberately
// slow run and a real rollout cannot be made slow on request.
//
// One flow, in order: a dev run at 1.4.2 where api is overridden to 1.4.3, whose image exits at once, so the run
// ends partly succeeded; its log; a re-run once 1.4.3 has been replaced by a good image; a restore back to 1.4.1;
// then a prod run that waits for a second person's approval.

const baseURL = process.env.YUMDRAKE_E2E_AWS_BASE_URL ?? ''
const here = dirname(fileURLToPath(import.meta.url))
const seedBinary = resolve(here, '..', '.e2e-bin', 'seed')
const jobTitle = 'Deploy everything'
const deployer = {
  email: process.env.YUMDRAKE_E2E_AWS_DEPLOYER_EMAIL ?? '',
  password: process.env.YUMDRAKE_E2E_AWS_DEPLOYER_PASSWORD ?? '',
  name: process.env.YUMDRAKE_E2E_AWS_DEPLOYER_NAME ?? '',
}
const approver = {
  email: process.env.YUMDRAKE_E2E_AWS_APPROVER_EMAIL ?? '',
  password: process.env.YUMDRAKE_E2E_AWS_APPROVER_PASSWORD ?? '',
}
// A rollout takes the 15 s stability window at least, and Floci starts every task in a container.
const rollout = { timeout: 300_000 }

const problems: string[] = []

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus && problems.length > 0) {
    await testInfo.attach('browser-problems', { body: problems.join('\n'), contentType: 'text/plain' })
  }
  problems.length = 0
})

/** Signs in with a password that is set natively, so no step title shows it. */
async function signIn(page: Page, email: string, password: string) {
  await page.goto(`${baseURL}/sign-in`)
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').evaluate((element, secret) => {
    const input = element as HTMLInputElement
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, secret)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }, password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).not.toHaveURL(/sign-in/)
}

async function personPage(browser: Browser, email: string, password: string): Promise<Page> {
  const context = await browser.newContext({ viewport: { width: 1360, height: 900 } })
  const page = await context.newPage()
  page.on('pageerror', (error) => problems.push(`page error: ${error.message}`))
  await signIn(page, email, password)
  return page
}

async function openJob(page: Page) {
  await page.goto(`${baseURL}/jobs`)
  await page.getByRole('table', { name: 'Jobs' }).getByRole('link', { name: jobTitle }).click()
  await expect(page.getByRole('heading', { name: jobTitle, level: 1 })).toBeVisible()
}

/** From the job page: choose the environment and the group's version, and plan. Lands on the run page, planned. */
async function planRun(page: Page, environment: string, version: string, overrides: Record<string, string> = {}) {
  await openJob(page)
  await page.getByRole('tab', { name: 'Run' }).click()
  await expect(page.getByRole('heading', { name: 'Environment', level: 2 })).toBeVisible()
  await page.getByLabel('Environment', { exact: true }).selectOption({ label: `${environment} (${environment.startsWith('prod') ? 'prod' : 'dev'})` })
  await page.getByLabel('Everything version').selectOption(version)
  if (Object.keys(overrides).length > 0) {
    await page.getByRole('button', { name: 'Choose services' }).click()
    for (const [service, tag] of Object.entries(overrides)) {
      await page.getByLabel(`Version for ${service}`).selectOption(tag)
    }
  }
  await expect(page.getByText('3 services selected')).toBeVisible()
  await expect(page.getByText(`Everything: 3 services at ${version}`)).toBeVisible()
  for (const [service, tag] of Object.entries(overrides)) {
    await expect(page.getByText(`${service} at ${tag}`)).toBeVisible()
  }
  await page.getByRole('button', { name: 'Plan this run' }).click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByTestId('run-lead')).toHaveText('Review the plan, then start')
}

const runTitle = (page: Page) => page.getByRole('heading', { name: /^Run #\d+/ })

test('a deployment is planned, fails in one service, is re-run, restored, and a prod run is approved by a second person', async ({ browser }) => {
  test.setTimeout(30 * 60_000)
  expect(baseURL, 'the global setup must provide the Floci stack (YUMDRAKE_E2E_AWS_ENDPOINT)').not.toBe('')
  const page = await personPage(browser, deployer.email, deployer.password)

  // ---- Dev run: 1.4.2 for the group, api overridden to 1.4.3, whose image crashes ----
  await planRun(page, 'dev-1', '1.4.2', { api: '1.4.3' })
  const firstRunUrl = page.url()

  // The plan shows the version changes and, for a changed env file, which keys are added, removed and changed.
  const plan = page.locator('.plan-panel')
  await expect(plan).toContainText('Plan: 3 services')
  await expect(plan).toContainText('1.4.1')
  await expect(plan).toContainText('1.4.3')
  await expect(plan).toContainText('web.env')
  await expect(plan.getByText('NEW_FLAG')).toBeVisible()
  await expect(plan.getByText('OLD_FLAG')).toBeVisible()
  await expect(plan.getByText('API_KEY')).toBeVisible()

  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(page.getByTestId('run-lead')).not.toHaveText('Review the plan, then start')
  // The service strip fills while the page stays open: a marker set on the window survives only if nothing reloaded.
  await page.evaluate(() => {
    ;(window as unknown as { __noReload: boolean }).__noReload = true
  })
  const strip = page.getByRole('img', { name: /^3 services, \d+ updated$/ })
  await expect(strip.locator('[data-status="succeeded"]')).toHaveCount(2, rollout)

  // The run ends partly succeeded and says which service failed and why.
  await expect(runTitle(page).getByText('Partly succeeded')).toBeVisible(rollout)
  expect(await page.evaluate(() => (window as unknown as { __noReload?: boolean }).__noReload), 'the page was reloaded').toBe(true)
  const failed = page.getByRole('region', { name: 'What failed' })
  await expect(failed).toContainText('api')
  await expect(failed).toContainText('container app exited with 3')

  // Its log, narrowed to that service, has the exit code line.
  const consoleRegion = page.getByRole('region', { name: 'Console' })
  const logLines = consoleRegion.getByRole('log', { name: 'Log lines' })
  await expect(logLines).toContainText('Run created by')
  await failed.getByRole('button', { name: 'Show its log' }).click()
  await expect(page).toHaveURL(/service=api/)
  await expect(consoleRegion.getByText(/[1-9]\d* error lines?/)).toBeVisible()
  await expect(logLines).toContainText('exit 3')
  await expect(logLines).not.toContainText('Run created by')

  // ---- The image is fixed (1.4.3 now points at a good image), so the failed service can be re-run ----
  const seed = JSON.parse(process.env.YUMDRAKE_E2E_AWS_SEED ?? '{}') as { endpoint: string; suffix: string; dev_account: string; prod_account: string }
  execFileSync(seedBinary, ['--repair', '--endpoint', seed.endpoint, '--suffix', seed.suffix, '--dev-account', seed.dev_account, '--prod-account', seed.prod_account], {
    stdio: 'inherit',
    timeout: 180_000,
  })
  await page.goto(firstRunUrl)
  await expect(runTitle(page).getByText('Partly succeeded')).toBeVisible()
  await page.getByRole('button', { name: 'Re-run failed' }).click()
  await expect(page).not.toHaveURL(firstRunUrl)
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByTestId('run-lead')).toHaveText('Review the plan, then start')
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(runTitle(page).getByText('Succeeded', { exact: true })).toBeVisible(rollout)

  // ---- Restore the first run: a plan back to 1.4.1, and then the services run it again ----
  await page.goto(firstRunUrl)
  await page.getByRole('button', { name: 'Restore' }).click()
  const restoreDialog = page.getByRole('dialog', { name: /^Restore \d+ services?\?$/ })
  await restoreDialog.getByRole('button', { name: 'Plan the restore' }).click()
  await expect(page).not.toHaveURL(firstRunUrl)
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByTestId('run-lead')).toHaveText('Review the plan, then start')
  await expect(page.locator('.plan-panel')).toContainText('1.4.1')
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(runTitle(page).getByText('Succeeded', { exact: true })).toBeVisible(rollout)

  // ---- Prod run: needs approval; a second signed-in person approves it from the Approvals page ----
  await planRun(page, 'prod-1', '1.4.2')
  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await page.getByRole('dialog', { name: 'Start in prod-1?' }).getByRole('button', { name: 'Start in prod' }).click()
  await expect(runTitle(page).getByText('Waiting for approval')).toBeVisible()
  const prodRunUrl = page.url()

  const second = await personPage(browser, approver.email, approver.password)
  await second.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: /^Approvals/ }).click()
  await expect(second.getByRole('heading', { name: 'Approvals', level: 1 })).toBeVisible()
  const card = second.getByRole('article').filter({ hasText: `${deployer.name} wants to run` })
  await expect(card).toContainText('prod-1')
  await card.getByRole('link', { name: 'Review and approve' }).click()
  await expect(second).toHaveURL(prodRunUrl)
  await second.getByRole('button', { name: 'Approve', exact: true }).click()

  // It deploys: the requester's page follows without a reload.
  await expect(runTitle(page).getByText('Succeeded', { exact: true })).toBeVisible(rollout)
  await expect(runTitle(second).getByText('Succeeded', { exact: true })).toBeVisible(rollout)
  expect(problems, 'the browser reported errors').toEqual([])
})
