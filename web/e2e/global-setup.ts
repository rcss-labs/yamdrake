import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { createInterface } from 'node:readline'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '..', '..')
const helperPath = resolve(repoRoot, 'web', '.e2e-bin', 'webstack')
const startLimitMs = 120_000
// Seeding two accounts of three services each into Floci takes a minute or more.
const awsStartLimitMs = 360_000
const stopLimitMs = 40_000

interface Ready {
  base_url: string
  email: string
  password: string
  runner_pid: number
  /** Only from a stack started with --aws-endpoint. */
  aws?: {
    endpoint: string
    suffix: string
    dev_account: string
    prod_account: string
    platform_account: string
    deployer_email: string
    deployer_name: string
    deployer_password: string
    approver_email: string
    approver_name: string
    approver_password: string
  }
}

/** Waits for the helper's one JSON line; rejects if it exits first or takes too long. */
function readReady(child: ChildProcess, limitMs: number): Promise<Ready> {
  return new Promise((resolveReady, reject) => {
    if (!child.stdout) {
      reject(new Error('the webstack helper has no standard output'))
      return
    }
    const timer = setTimeout(() => reject(new Error('the webstack helper was not ready in time')), limitMs)
    const lines = createInterface({ input: child.stdout })
    lines.once('line', (line) => {
      clearTimeout(timer)
      try {
        resolveReady(JSON.parse(line) as Ready)
      } catch {
        reject(new Error('the webstack helper printed something that is not JSON'))
      }
    })
    child.once('exit', (code) => {
      clearTimeout(timer)
      reject(new Error(`the webstack helper exited with code ${code ?? 'null'} before it was ready`))
    })
  })
}

function stop(child: ChildProcess): Promise<void> {
  return new Promise((resolveStop) => {
    if (child.exitCode !== null || child.signalCode !== null) {
      resolveStop()
      return
    }
    const killer = setTimeout(() => child.kill('SIGKILL'), stopLimitMs)
    child.once('exit', () => {
      clearTimeout(killer)
      resolveStop()
    })
    child.kill('SIGTERM')
  })
}

/** Starts one webstack helper and waits for its ready line. */
async function startStack(args: string[], limitMs: number): Promise<{ child: ChildProcess; ready: Ready }> {
  const child = spawn(helperPath, args, { cwd: repoRoot, stdio: ['pipe', 'pipe', 'inherit'] })
  const onSignal = () => {
    void stop(child)
  }
  process.once('SIGINT', onSignal)
  process.once('SIGTERM', onSignal)
  process.once('exit', () => child.kill('SIGTERM'))
  try {
    return { child, ready: await readReady(child, limitMs) }
  } catch (error) {
    await stop(child)
    throw error
  }
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  // A helper binary (not `go run`) so signals reach the helper itself, which then stops the orchestrator.
  mkdirSync(dirname(helperPath), { recursive: true })
  execFileSync('go', ['build', '-o', helperPath, './test/e2e/webstack'], { cwd: repoRoot, stdio: 'inherit' })
  // The runner with fake steps only. The production runner wires the real resolve, which needs AWS.
  execFileSync('go', ['build', '-o', resolve(dirname(helperPath), 'webrunner'), './test/e2e/webrunner'], { cwd: repoRoot, stdio: 'inherit' })

  // The seeding command, which aws-runs.spec.ts runs to repair the crashing version between a failed run and its re-run.
  execFileSync('go', ['build', '-o', resolve(dirname(helperPath), 'seed'), './test/awstest/cmd/seed'], { cwd: repoRoot, stdio: 'inherit' })

  // The helper stops when its standard input closes, so it also stops if this process dies.
  const { child, ready } = await startStack([], startLimitMs)
  process.env.YUMDRAKE_E2E_BASE_URL = ready.base_url
  process.env.YUMDRAKE_E2E_EMAIL = ready.email
  process.env.YUMDRAKE_E2E_PASSWORD = ready.password
  // The runner's process id, so a spec can stop and restart it (see e2e/runner.ts).
  process.env.YUMDRAKE_E2E_RUNNER_PID = String(ready.runner_pid)

  // A second stack for aws-runs.spec.ts: the production runner and real steps against Floci. It is started only
  // when an endpoint is given (CI and `make web-e2e` give one); in CI a missing endpoint is an error, not a skip.
  const awsEndpoint = process.env.YUMDRAKE_E2E_AWS_ENDPOINT ?? ''
  if (awsEndpoint === '' && process.env.CI) {
    await stop(child)
    throw new Error('YUMDRAKE_E2E_AWS_ENDPOINT must be set in CI: aws-runs.spec.ts needs Floci')
  }
  let awsChild: ChildProcess | undefined
  if (awsEndpoint !== '') {
    try {
      const aws = await startStack(['--aws-endpoint', awsEndpoint], awsStartLimitMs)
      awsChild = aws.child
      const info = aws.ready.aws
      if (!info) throw new Error('the webstack helper gave no AWS details')
      process.env.YUMDRAKE_E2E_AWS_BASE_URL = aws.ready.base_url
      process.env.YUMDRAKE_E2E_AWS_ADMIN_EMAIL = aws.ready.email
      process.env.YUMDRAKE_E2E_AWS_ADMIN_PASSWORD = aws.ready.password
      process.env.YUMDRAKE_E2E_AWS_DEPLOYER_EMAIL = info.deployer_email
      process.env.YUMDRAKE_E2E_AWS_DEPLOYER_NAME = info.deployer_name
      process.env.YUMDRAKE_E2E_AWS_DEPLOYER_PASSWORD = info.deployer_password
      process.env.YUMDRAKE_E2E_AWS_APPROVER_EMAIL = info.approver_email
      process.env.YUMDRAKE_E2E_AWS_APPROVER_NAME = info.approver_name
      process.env.YUMDRAKE_E2E_AWS_APPROVER_PASSWORD = info.approver_password
      // What the spec passes to the seed command to repair the crashing version.
      process.env.YUMDRAKE_E2E_AWS_SEED = JSON.stringify({
        endpoint: info.endpoint,
        suffix: info.suffix,
        dev_account: info.dev_account,
        prod_account: info.prod_account,
      })
    } catch (error) {
      await stop(child)
      throw error
    }
  }

  return async () => {
    if (awsChild) await stop(awsChild)
    await stop(child)
  }
}
