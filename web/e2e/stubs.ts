import type { Page } from '@playwright/test'

// Temporary stubs for API routes that the plans not yet merged (Plan 5: versions and the plan; Plan 7:
// approvals and freezes) have not built. The web app calls them already, so without a stub the browser sees a 404.
// Everything else goes to the real orchestrator and runner. Remove each stub when its route exists.

/** The freeze banner (Plan 7): no freeze is active. */
export async function stubFreezes(page: Page) {
  await page.route('**/api/v1/freezes', (route) => route.fulfill({ json: { active: [], ended: [] } }))
}

/** The pending-approvals count in the sidebar: nothing is waiting. */
export async function stubApprovals(page: Page) {
  await page.route('**/api/v1/approvals', (route) => route.fulfill({ json: { approvals: [] } }))
}

/** What the Run tab's plan form reads (Plan 5). 1.0.0 is good, 1.0.0-fail fails the roll, 1.0.0-slow takes 30 s. */
export const tags = ['1.0.0', '1.0.0-fail', '1.0.0-slow']
const versionList = (service: string) => tags.map((tag) => ({ tag, digest: `sha256:${'a'.repeat(63)}1`, pushed_at: '2026-11-27T10:00:00Z', service }))

/** The versions the plan screen shows; the test sets them to what it chose in the form. */
export const planned = { backends: '1.0.0' }

export async function stubPlanRoutes(page: Page) {
  await page.route('**/api/v1/jobs/*/versions?*', (route) =>
    route.fulfill({
      json: {
        groups: {
          backends: { common: tags, services: { 'backend-api': versionList('backend-api') }, unavailable: {} },
          microservices: { common: [], services: {}, unavailable: { orders: 'not in this environment' } },
        },
      },
    }),
  )
  await page.route('**/api/v1/runs/*/plan', async (route) => {
    const runId = Number(new URL(route.request().url()).pathname.split('/')[4])
    const service = (name: string, group: string, version: string) => ({
      name,
      group,
      version,
      image_digest: `sha256:${'a'.repeat(63)}1`,
      ecs_service: `arn:aws:ecs:eu-central-1:111111111111:service/hetzner-01/${name}`,
      ecr_repository: `111111111111.dkr.ecr.eu-central-1.amazonaws.com/${name}`,
      previous_task_definition: '',
      previous_version: '',
      config: [],
    })
    await route.fulfill({
      json: {
        id: `plan-${runId}`,
        run_id: runId,
        environment: 'hetzner-01',
        label: 'dev',
        aws_account: '111111111111',
        inventory_commit: '1111111111111111111111111111111111111111',
        job_commit: '1111111111111111111111111111111111111111',
        services: [service('backend-api', 'backends', planned.backends)],
        checks: [],
      },
    })
  })
}
