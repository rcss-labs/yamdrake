import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

const baseURL = process.env.YUMDRAKE_E2E_BASE_URL ?? ''
const email = process.env.YUMDRAKE_E2E_EMAIL ?? ''
const password = process.env.YUMDRAKE_E2E_PASSWORD ?? ''

const jobTitle = 'Backend and microservices'
const folderName = 'Backend'
const groupName = 'Release managers'

/** Screenshots go to YUMDRAKE_E2E_SHOTS_DIR when set, otherwise into Playwright's output folder. */
async function shot(page: Page, testOutputDir: string, name: string) {
  const dir = process.env.YUMDRAKE_E2E_SHOTS_DIR ?? testOutputDir
  mkdirSync(dir, { recursive: true })
  // Animations off so the sidebar's highlight transition has settled in the picture.
  await page.screenshot({ path: resolve(dir, `${name}.png`), fullPage: true, animations: 'disabled' })
}

// Browser problems seen so far. They are attached to a failed test, since they are often the root cause.
const problems: string[] = []
let tracing = false

test.afterEach(async ({ context }, testInfo) => {
  if (tracing) {
    // Saved only on failure, and only from after the sign-in, so no trace holds the password.
    const path = testInfo.status === testInfo.expectedStatus ? undefined : testInfo.outputPath('trace.zip')
    await context.tracing.stop(path ? { path } : undefined)
    if (path) await testInfo.attach('trace-after-sign-in', { path, contentType: 'application/zip' })
    tracing = false
  }
  if (testInfo.status !== testInfo.expectedStatus && problems.length > 0) {
    await testInfo.attach('browser-problems', { body: problems.join('\n'), contentType: 'text/plain' })
  }
  problems.length = 0
})

test('a person signs in, reads jobs and inventory, manages access and signs out', async ({ page, context }, testInfo) => {
  expect(baseURL, 'the global setup must provide the orchestrator address').not.toBe('')

  // Any console error (a CSP violation is one) or uncaught page error fails the test.
  // The only expected failure: asking who is signed in (GET /api/v1/me) while nobody is, which answers 401.
  // Chromium logs that as a console error, so it is excused by address; every other 4xx or 5xx answer fails too.
  const isSignedOutProbe = (url: string, status: number) => status === 401 && new URL(url).pathname === '/api/v1/me'
  page.on('console', (message) => {
    if (message.type() !== 'error') return
    const where = message.location().url
    if (message.text().includes('status of 401') && where !== '' && new URL(where).pathname === '/api/v1/me') return
    problems.push(`console error: ${message.text()}`)
  })
  page.on('response', (response) => {
    if (response.status() >= 400 && !isSignedOutProbe(response.url(), response.status())) {
      problems.push(`${response.status()} from ${new URL(response.url()).pathname}`)
    }
  })
  page.on('pageerror', (error) => problems.push(`page error: ${error.message}`))

  // Sign in. The session cookie is Secure; Chromium accepts it for http://127.0.0.1.
  await page.goto(`${baseURL}/jobs`)
  await expect(page).toHaveURL(/\/sign-in/)
  await expect(page.getByLabel('Email')).toBeVisible()
  await shot(page, testInfo.outputDir, '01-sign-in')
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()

  // Jobs: the registered job is listed under its folder, and the person is back where they were going.
  await expect(page).toHaveURL(/\/jobs$/)
  await expect(page.getByRole('heading', { name: 'Jobs', level: 1 })).toBeVisible()
  // From here on a trace is kept for a failure (it starts after the password was typed).
  await context.tracing.start({ screenshots: true, snapshots: true })
  tracing = true
  const jobsTable = page.getByRole('table', { name: 'Jobs' })
  // The folder's own heading row ("Backend 1 job") is followed directly by the job's row.
  await expect(jobsTable.getByRole('row', { name: `${folderName} 1 job`, exact: true })).toBeVisible()
  const jobLink = jobsTable.getByRole('link', { name: jobTitle })
  await expect(jobLink).toBeVisible()
  const rowTexts = await jobsTable.getByRole('row').allInnerTexts()
  const folderIndex = rowTexts.findIndex((text) => text.replace(/\s+/g, ' ').trim() === `${folderName} 1 job`)
  expect(folderIndex, 'the folder heading row').toBeGreaterThan(0)
  expect(rowTexts[folderIndex + 1], 'the row after the folder heading').toContain(jobTitle)
  await shot(page, testInfo.outputDir, '02-jobs')

  // The job: its steps and the YAML source.
  await jobLink.click()
  await expect(page.getByRole('heading', { name: jobTitle, level: 1 })).toBeVisible()
  await expect(page.getByRole('tab', { name: 'Definition', selected: true })).toBeVisible()
  for (const step of ['config-backends', 'roll-backends', 'config-microservices', 'roll-microservices']) {
    await expect(page.getByRole('listitem', { name: step })).toBeVisible()
  }
  await expect(page.getByRole('listitem', { name: 'Backend services', exact: true })).toBeVisible()
  await expect(page.getByRole('listitem', { name: 'Microservices', exact: true })).toBeVisible()
  const yaml = page.getByRole('region', { name: 'Job file' })
  await expect(yaml).toContainText('job: deploy-backend')
  await expect(yaml).toContainText('use: ecs-deploy')
  await shot(page, testInfo.outputDir, '03-job-definition')

  // Inventory: five environments, then a service's blocks.
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Inventory' }).click()
  await expect(page.getByRole('heading', { name: 'Inventory', level: 1 })).toBeVisible()
  const environments = page.getByRole('table', { name: 'Environments' })
  await expect(environments.getByRole('row')).toHaveCount(6) // header and five environments
  for (const name of ['hetzner-01', 'dev-02', 'dev-03', 'preprod-01', 'prod-01']) {
    await expect(environments.getByRole('cell', { name, exact: true })).toBeVisible()
  }
  await shot(page, testInfo.outputDir, '04-inventory-environments')

  await page.getByRole('tab', { name: 'Services' }).click()
  const services = page.getByRole('list', { name: 'Services' })
  await expect(services.getByRole('listitem')).toHaveCount(2)
  await services.getByRole('button', { name: /backend-api/ }).click()
  await expect(page.getByRole('heading', { name: 'backend-api', level: 2 })).toBeVisible()
  await expect(page.getByRole('region', { name: 'hetzner-01' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'prod-01' })).toBeVisible()
  await shot(page, testInfo.outputDir, '05-inventory-services')

  // Admin users: the administrator made by create-admin is listed.
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' }).click()
  await expect(page.getByRole('heading', { name: 'Admin', level: 1 })).toBeVisible()
  await expect(page.getByRole('tab', { name: 'Users', selected: true })).toBeVisible()
  await expect(page.getByRole('table', { name: 'Users' }).getByText(email)).toBeVisible()
  await shot(page, testInfo.outputDir, '06-admin-users')

  // Groups: create a group and a grant.
  await page.getByRole('tab', { name: 'Groups and permissions' }).click()
  await page.getByLabel('Group name').fill(groupName)
  await page.getByRole('button', { name: 'Create group' }).click()
  const card = page.getByRole('region', { name: groupName })
  await expect(card).toBeVisible()
  await card.getByRole('button', { name: 'Add grant' }).click()
  await expect(card.getByRole('table', { name: `Grants of ${groupName}` }).getByRole('cell', { name: 'Run jobs' })).toBeVisible()
  await shot(page, testInfo.outputDir, '07-admin-groups')

  // Audit: both changes were recorded.
  await page.getByRole('tab', { name: 'Audit log' }).click()
  const audit = page.getByRole('table', { name: 'Audit log' })
  // The Administrators group made by create-admin has its own group.create entry, so match ours by name.
  const groupEntry = audit.getByRole('row').filter({ hasText: 'group.create' }).filter({ hasText: groupName })
  await expect(groupEntry).toHaveCount(1)
  await expect(groupEntry).toContainText(email)
  await expect(audit.getByRole('row').filter({ hasText: 'grant.create' })).toHaveCount(1)
  await shot(page, testInfo.outputDir, '08-audit')

  // Sign out leads back to the sign-in page, and a protected page needs signing in again.
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/sign-in/)
  await expect(page.getByLabel('Email')).toBeVisible()
  await page.goto(`${baseURL}/admin`)
  await expect(page).toHaveURL(/\/sign-in/)

  expect(problems, 'the browser reported errors').toEqual([])
})
