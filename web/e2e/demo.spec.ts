import { expect, test } from '@playwright/test'

// The demo in a real browser (scripts/demo-check.sh runs this against the demo it started, through
// playwright.demo.config.ts): the deployer signs in with the demo's public sign-in, picks a version in the version
// picker, sees what the plan changes, starts the run and watches it succeed. The runner behind it runs the real
// steps against the demo's local AWS. qa is used because the script's API and CLI checks work in dev.

const baseURL = process.env.YUMDRAKE_DEMO_URL ?? ''
const email = 'deployer@demo.yumdrake.test'
const password = 'demo-deployer-password' // public: it is printed in the README

test('the deployer picks a version, reads the plan, starts the run and it succeeds', async ({ page }) => {
  expect(baseURL, 'YUMDRAKE_DEMO_URL must be the address of the running demo').not.toBe('')
  const problems: string[] = []
  page.on('pageerror', (error) => problems.push(`page error: ${error.message}`))

  await page.goto(`${baseURL}/sign-in`)
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).not.toHaveURL(/sign-in/)

  await page.goto(`${baseURL}/jobs`)
  await page.getByRole('table', { name: 'Jobs' }).getByRole('link', { name: 'Deploy the web frontend' }).click()
  await page.getByRole('tab', { name: 'Run' }).click()
  await expect(page.getByRole('heading', { name: 'Environment', level: 2 })).toBeVisible()
  await page.getByLabel('Environment', { exact: true }).selectOption({ label: 'qa (preprod)' })

  // The version picker offers the versions the local AWS really holds, the newest first.
  const picker = page.getByLabel('Web frontend version')
  await expect(picker.locator('option')).toHaveText(['1.4.2 (newest)', '1.4.1'])
  await picker.selectOption('1.4.2')
  await expect(page.getByText('Web frontend: 1 service at 1.4.2')).toBeVisible()
  await page.getByRole('button', { name: 'Plan this run' }).click()

  // The plan shows the change: the version, and which keys of the env file change.
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByTestId('run-lead')).toHaveText('Review the plan, then start')
  const plan = page.locator('.plan-panel')
  await expect(plan).toContainText('1.4.1')
  await expect(plan).toContainText('1.4.2')
  await expect(plan).toContainText('web.env')
  await expect(plan.getByText('NEW_FLAG')).toBeVisible()

  await page.getByRole('button', { name: 'Start', exact: true }).click()
  await expect(page.getByRole('heading', { name: /^Run #\d+/ }).getByText('Succeeded', { exact: true })).toBeVisible({ timeout: 240_000 })
  expect(problems, 'the browser reported errors').toEqual([])
})
