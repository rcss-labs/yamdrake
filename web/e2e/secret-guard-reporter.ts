import type { FullConfig, FullResult, Reporter } from '@playwright/test/reporter'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'

/**
 * Fails the run if the sign-in password appears anywhere in what CI uploads: the HTML report (which embeds a zip as
 * base64) and the test results (traces are zips). It must be listed after the html reporter, whose files it reads.
 */
export default class SecretGuardReporter implements Reporter {
  private root = process.cwd()

  onBegin(config: FullConfig) {
    if (config.configFile) this.root = dirname(config.configFile)
  }

  async onEnd(): Promise<{ status: FullResult['status'] } | undefined> {
    // The password of the fake-step stack, and the three of the stack that runs against Floci (aws-runs.spec.ts).
    const names = ['YUMDRAKE_E2E_PASSWORD', 'YUMDRAKE_E2E_AWS_ADMIN_PASSWORD', 'YUMDRAKE_E2E_AWS_DEPLOYER_PASSWORD', 'YUMDRAKE_E2E_AWS_APPROVER_PASSWORD']
    const hits: string[] = []
    for (const name of names) {
      const password = process.env[name] ?? ''
      if (password === '') continue
      for (const dir of ['playwright-report', 'test-results']) scanTree(resolve(this.root, dir), password, hits)
    }
    if (hits.length === 0) return undefined
    console.error(`secret guard: a sign-in password was found in ${hits.length} place(s):\n  ${hits.join('\n  ')}`)
    return { status: 'failed' }
  }
}

function scanTree(path: string, password: string, hits: string[]) {
  let stat
  try {
    stat = statSync(path)
  } catch {
    return
  }
  if (stat.isDirectory()) {
    for (const name of readdirSync(path)) scanTree(join(path, name), password, hits)
    return
  }
  const data = readFileSync(path)
  if (data.includes(password)) hits.push(path)
  if (path.endsWith('.zip')) scanZip(path, password, hits, path)
  if (path.endsWith('.html')) {
    // The HTML report keeps its data as a base64 zip inside index.html.
    for (const match of data.toString('latin1').matchAll(/data:application\/zip;base64,([A-Za-z0-9+/=]+)/g)) {
      const temp = mkdtempSync(join(tmpdir(), 'secret-guard-'))
      try {
        const zipPath = join(temp, 'report.zip')
        writeFileSync(zipPath, Buffer.from(match[1] ?? '', 'base64'))
        scanZip(zipPath, password, hits, `${path} (embedded report data)`)
      } finally {
        rmSync(temp, { recursive: true, force: true })
      }
    }
  }
}

function scanZip(zipPath: string, password: string, hits: string[], label: string) {
  // unzip is on macOS and on the Ubuntu CI image; a failure to run it is itself a failure of the guard.
  const contents = execFileSync('unzip', ['-p', zipPath], { maxBuffer: 512 * 1024 * 1024 })
  if (contents.includes(password)) hits.push(label)
}
