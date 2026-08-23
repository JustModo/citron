#!/usr/bin/env node
// Cross-version smoke test: starts the container, drives the real API, and checks
// that the cgroup-enforced behaviour is identical whether the host runs cgroup v1
// or v2. Run it on both kinds of host and compare the two reports.
//
//   node tests/smoke/smoke.mjs              start the container, test, tear down
//   node tests/smoke/smoke.mjs --keep       leave it running afterwards
//   node tests/smoke/smoke.mjs --no-up      test an instance that is already up
//   node tests/smoke/smoke.mjs --url http://host:2358
//
// Needs Node 18+ (built-in fetch) and nothing else.

import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const exec = promisify(execFile)
const argv = process.argv.slice(2)
const flag = (name) => argv.includes(name)
const opt = (name, fallback) => {
  const i = argv.indexOf(name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

const BASE = opt('--url', 'http://127.0.0.1:2358')
const START = !flag('--no-up')
const KEEP = flag('--keep')
const PY = 71

const sh = async (cmd, args) => {
  try {
    const { stdout } = await exec(cmd, args, { maxBuffer: 32 << 20 })
    return stdout.trim()
  } catch (e) {
    return (e.stdout || '').trim()
  }
}

// --- environment ------------------------------------------------------------

async function describeEnvironment() {
  const hostFS = await sh('stat', ['-fc', '%T', '/sys/fs/cgroup'])
  const host = hostFS === 'cgroup2fs' ? 'v2' : hostFS ? 'v1 / hybrid' : 'unknown'

  // The service logs which backend it resolved at startup. That line is the whole
  // point of this script: it tells you which code path the checks below exercised.
  const logs = await sh('docker', ['compose', 'logs', '--no-color', 'citron'])
  const line = logs.split('\n').reverse().find((l) => l.includes('cgroup backend'))
  // The log line is JSON ("version":"v2") under docker compose and logfmt
  // (version=v2) when the binary is run directly, so accept either spelling.
  const inContainer = line?.match(/version["':=\s]+([\w.]+)/)?.[1] ?? 'unknown'

  return { hostFS, host, inContainer, line: line?.trim() }
}

// --- api --------------------------------------------------------------------

async function submit(body) {
  const res = await fetch(`${BASE}/submissions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const text = await res.text()
  let json
  try {
    json = JSON.parse(text)
  } catch {
    throw new Error(`non-JSON response (HTTP ${res.status}): ${text.slice(0, 300)}`)
  }
  if (res.status >= 500) throw new Error(`HTTP ${res.status}: ${text.slice(0, 300)}`)
  return json
}

const python = (source, testcases, limits = {}) =>
  submit({ language_id: PY, source_code: source, testcases, ...limits })

async function waitForReady(timeoutMs = 180_000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    try {
      const res = await fetch(`${BASE}/ready`)
      if (res.ok) return
    } catch {
      // not listening yet
    }
    if (Date.now() > deadline) throw new Error(`${BASE}/ready never came up`)
    await new Promise((r) => setTimeout(r, 1000))
  }
}

// --- checks -----------------------------------------------------------------
// Each returns a detail string on success or throws. They are deliberately about
// behaviour the cgroup enforces, not about Python.

const checks = [
  {
    name: 'accepted, with cgroup accounting',
    why: 'proves the process was placed in the cgroup and the counters read back',
    async run() {
      const r = await python('print(2 + 3)', [{ stdin: '', expected_output: '5' }])
      expect(r.status.description, 'Accepted', 'submission status')
      const tc = r.testcases[0]
      expect(tc.status.description, 'Accepted', 'testcase status')
      expect(tc.stdout.trim(), '5', 'stdout')
      if (tc.memory_source !== 'cgroup') {
        throw new Error(`memory_source = ${tc.memory_source}, want "cgroup"`)
      }
      if (!(tc.memory_kb > 0)) throw new Error(`memory_kb = ${tc.memory_kb}, want > 0`)
      return `${tc.memory_kb} KB, ${tc.cpu_time_ms} ms cpu, source=${tc.memory_source}`
    },
  },
  {
    name: 'wrong answer',
    why: 'the comparison path still works around the sandbox changes',
    async run() {
      const r = await python('print(4)', [{ stdin: '', expected_output: '5' }])
      expect(r.status.description, 'Wrong Answer', 'status')
      return `stdout=${JSON.stringify(r.testcases[0].stdout.trim())}`
    },
  },
  {
    name: 'memory limit enforced',
    why: 'the cgroup memory controller kills a bomb instead of the host swapping',
    async run() {
      const r = await python(
        'x = bytearray(400 * 1024 * 1024)\nprint(len(x))',
        [{ stdin: '', expected_output: 'unreachable' }],
        { memory_limit: 64 * 1024 },
      )
      const got = r.status.description
      if (got !== 'Memory Limit Exceeded' && !got.startsWith('Runtime Error')) {
        throw new Error(`status = ${got}, want Memory Limit Exceeded`)
      }
      return `${got} at ${r.testcases[0].memory_kb} KB`
    },
  },
  {
    name: 'cpu limit enforced',
    why: 'cpu accounting comes from the cgroup, so a spin must still time out',
    async run() {
      const r = await python(
        'while True:\n    pass',
        [{ stdin: '', expected_output: 'unreachable' }],
        { cpu_time_limit: 1, wall_time_limit: 5 },
      )
      expect(r.status.description, 'Time Limit Exceeded', 'status')
      return `killed after ${r.testcases[0].cpu_time_ms} ms cpu`
    },
  },
  {
    name: 'process limit enforced',
    why: 'pids.max must contain a fork bomb on either cgroup version',
    async run() {
      const source = [
        'import os',
        'for _ in range(500):',
        '    try:',
        '        os.fork()',
        '    except OSError:',
        '        pass',
        'print("survived")',
      ].join('\n')
      const r = await python(source, [{ stdin: '', expected_output: 'survived' }], {
        cpu_time_limit: 2,
        wall_time_limit: 8,
      })
      // Any verdict is fine; what matters is that it terminated rather than taking
      // the host with it, and that the service is still answering afterwards.
      const ready = await fetch(`${BASE}/ready`)
      if (!ready.ok) throw new Error('service stopped being ready after a fork bomb')
      return `${r.status.description}, service still ready`
    },
  },
  {
    name: 'kill takes the whole tree',
    why: 'a child that outlives its parent must die with the cgroup, not linger',
    async run() {
      const source = [
        'import os, sys, time',
        'if os.fork() == 0:',
        '    time.sleep(300)',
        '    sys.exit(0)',
        'print("parent done")',
        'sys.stdout.flush()',
        'time.sleep(300)',
      ].join('\n')
      const started = Date.now()
      const r = await python(source, [{ stdin: '', expected_output: 'parent done' }], {
        cpu_time_limit: 1,
        wall_time_limit: 3,
      })
      const elapsed = Date.now() - started
      if (elapsed > 30_000) {
        throw new Error(`took ${elapsed} ms; the orphan was not cleaned up promptly`)
      }
      return `${r.status.description} in ${elapsed} ms`
    },
  },
  {
    name: 'runtime error reported',
    why: 'a non-zero exit is a verdict, not a sandbox failure',
    async run() {
      const r = await python('raise SystemExit(3)', [{ stdin: '', expected_output: '' }])
      if (!r.status.description.startsWith('Runtime Error')) {
        throw new Error(`status = ${r.status.description}, want a runtime error`)
      }
      return `${r.status.description}, exit=${r.testcases[0].exit_code}`
    },
  },
  {
    name: 'many testcases, one compile',
    why: 'concurrent executions each get their own cgroup and do not collide',
    async run() {
      const testcases = Array.from({ length: 12 }, (_, i) => ({
        stdin: String(i),
        expected_output: String(i * i),
      }))
      const r = await python('n = int(input())\nprint(n * n)', testcases)
      expect(r.status.description, 'Accepted', 'status')
      expect(r.testcases.length, testcases.length, 'testcase count')
      const unaccounted = r.testcases.filter((t) => !(t.memory_kb > 0))
      if (unaccounted.length) {
        throw new Error(`${unaccounted.length} testcases reported no memory`)
      }
      return `${r.testcases.length}/${testcases.length} accepted, all accounted`
    },
  },
  {
    name: 'stdin is delivered',
    why: 'the sandbox plumbing around the cgroup still wires the pipes',
    async run() {
      const r = await python('print(sum(int(x) for x in input().split()))', [
        { stdin: '1 2 3 4', expected_output: '10' },
      ])
      expect(r.status.description, 'Accepted', 'status')
      return 'stdin echoed correctly'
    },
  },
]

function expect(got, want, what) {
  if (got !== want) throw new Error(`${what} = ${JSON.stringify(got)}, want ${JSON.stringify(want)}`)
}

// --- main -------------------------------------------------------------------

async function main() {
  if (START) {
    console.log('starting container (docker compose up -d --build) ...')
    await exec('docker', ['compose', 'up', '-d', '--build'], { maxBuffer: 64 << 20 })
  }
  console.log(`waiting for ${BASE}/ready ...`)
  await waitForReady()

  const env = await describeEnvironment()
  console.log('')
  console.log(`host /sys/fs/cgroup : ${env.hostFS} (${env.host})`)
  console.log(`citron chose        : cgroup ${env.inContainer}`)
  if (env.line) console.log(`startup log         : ${env.line}`)
  console.log('')

  let failed = 0
  for (const check of checks) {
    process.stdout.write(`  ${check.name.padEnd(34)}`)
    try {
      const detail = await check.run()
      console.log(`PASS  ${detail}`)
    } catch (e) {
      failed++
      console.log(`FAIL  ${e.message}`)
      console.log(`        ${check.why}`)
    }
  }

  console.log('')
  console.log(`${checks.length - failed}/${checks.length} passed on cgroup ${env.inContainer}`)
  if (START && !KEEP) await exec('docker', ['compose', 'down'])
  process.exit(failed ? 1 : 0)
}

main().catch(async (e) => {
  console.error(`\nsmoke test could not run: ${e.message}`)
  if (START && !KEEP) await exec('docker', ['compose', 'down']).catch(() => {})
  process.exit(2)
})
