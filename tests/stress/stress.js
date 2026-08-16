#!/usr/bin/env node

/**
 * citron load harness.
 * Runs concurrent virtual users issuing multi-testcase submissions to citron.
 * Automatically manages docker profile lifecycle when requested.
 */

const http = require('http');
const https = require('https');
const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');
const { getSubmissionPayload } = require('./questions');
const { ProgressBar } = require('./progress');
const { generateReport, printReportTable } = require('./reporter');

const DEFAULT_CONFIG = {
  url: process.env.CITRON_URL || 'http://127.0.0.1:2358',
  users: 100,
  submissionsPerUser: 1,
  testcasesPerSubmission: 100,
  tier: 'all',
  profile: 'baseline',
  rampUpMs: 0,
  timeoutMs: 60000,
  jsonOutput: false,
  manageDocker: true,
};

/**
 * Parses command-line arguments into configuration object.
 * @returns {Object}
 */
function parseArgs() {
  const args = process.argv.slice(2);
  const config = { ...DEFAULT_CONFIG };

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--help' || arg === '-h') {
      printHelp();
      process.exit(0);
    } else if (arg === '--users' && args[i + 1]) {
      config.users = parseInt(args[++i], 10);
    } else if (arg === '--submissions-per-user' && args[i + 1]) {
      config.submissionsPerUser = parseInt(args[++i], 10);
    } else if (arg === '--testcases-per-submission' && args[i + 1]) {
      config.testcasesPerSubmission = parseInt(args[++i], 10);
    } else if (arg === '--url' && args[i + 1]) {
      config.url = args[++i];
    } else if (arg === '--tier' && args[i + 1]) {
      config.tier = args[++i];
    } else if (arg === '--profile' && args[i + 1]) {
      config.profile = args[++i];
    } else if (arg === '--ramp-up' && args[i + 1]) {
      config.rampUpMs = parseInt(args[++i], 10);
    } else if (arg === '--timeout' && args[i + 1]) {
      config.timeoutMs = parseInt(args[++i], 10);
    } else if (arg === '--json') {
      config.jsonOutput = true;
    } else if (arg === '--no-manage-docker' || arg === '--standalone') {
      config.manageDocker = false;
    }
  }
  return config;
}

/**
 * Prints CLI usage instructions.
 */
function printHelp() {
  console.log(`
Usage: node tests/stress/stress.js [options]

Options:
  --users <n>                     Virtual user concurrency (default: 100)
  --submissions-per-user <n>      Submissions per virtual user (default: 1)
  --testcases-per-submission <n>  Testcases per submission payload (default: 100)
  --url <url>                     Base URL (default: http://127.0.0.1:2358)
  --tier <light|medium|heavy|all> Problem computational tier (default: all)
  --profile <baseline|medium|high|unlimited> Target profile (default: baseline)
  --ramp-up <ms>                  Ramp up delay (default: 0)
  --timeout <ms>                  Request timeout in ms (default: 60000)
  --standalone, --no-manage-docker Skip Docker lifecycle management
  --json                          Export JSON report to stress-results.json
  --help, -h                      Show options
`);
}

/**
 * Resolves the compose file path for a profile.
 * @param {string} profile
 * @returns {string}
 */
function getComposeFilePath(profile) {
  const profilePath = path.join(__dirname, 'profiles', `${profile}.yaml`);
  if (fs.existsSync(profilePath)) return profilePath;
  const altPath = path.join(__dirname, 'profiles', `compose.${profile}.yaml`);
  if (fs.existsSync(altPath)) return altPath;
  return path.join(__dirname, `compose.${profile}.yaml`);
}

/**
 * Starts Docker container for the profile.
 * @param {string} profile
 */
function startDockerProfile(profile) {
  const composeFile = getComposeFilePath(profile);
  if (!fs.existsSync(composeFile)) {
    throw new Error(`Compose profile file not found: ${composeFile}`);
  }
  console.log(`[Docker] Starting citron profile '${profile}'...`);
  execSync(`docker compose -f "${composeFile}" up -d`, { stdio: 'inherit' });
}

/**
 * Stops Docker container for the profile.
 * @param {string} profile
 */
function stopDockerProfile(profile) {
  const composeFile = getComposeFilePath(profile);
  if (fs.existsSync(composeFile)) {
    console.log(`\n[Docker] Stopping citron profile '${profile}'...`);
    try {
      execSync(`docker compose -f "${composeFile}" down`, { stdio: 'inherit' });
    } catch (err) {
      console.error(`[Docker] Cleanup warning:`, err.message);
    }
  }
}

/**
 * Polls citron health endpoint until healthy.
 * @param {string} baseUrl
 * @param {number} [maxWaitSec=40]
 * @returns {Promise<boolean>}
 */
async function waitForServerReady(baseUrl, maxWaitSec = 40) {
  process.stdout.write(`[Health] Waiting for citron at ${baseUrl}... `);
  const healthUrl = new URL('/health', baseUrl);
  const startTime = Date.now();

  while ((Date.now() - startTime) / 1000 < maxWaitSec) {
    const isUp = await new Promise((resolve) => {
      const transport = healthUrl.protocol === 'https:' ? https : http;
      const req = transport.get(healthUrl, { timeout: 2000 }, (res) => {
        resolve(res.statusCode === 200);
      });
      req.on('error', () => resolve(false));
      req.on('timeout', () => { req.destroy(); resolve(false); });
    });

    if (isUp) {
      console.log(`ready.`);
      return true;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
  throw new Error(`Server at ${baseUrl} failed to respond within ${maxWaitSec}s`);
}

/**
 * Sends a single POST /submissions request to citron.
 * @param {string} url
 * @param {Object} payload
 * @param {http.Agent} httpAgent
 * @param {number} timeoutMs
 * @returns {Promise<Object>}
 */
function sendSubmission(url, payload, httpAgent, timeoutMs) {
  return new Promise((resolve) => {
    const targetUrl = new URL('/submissions', url);
    const postData = Buffer.from(JSON.stringify(payload));
    const transport = targetUrl.protocol === 'https:' ? https : http;

    const reqOpts = {
      hostname: targetUrl.hostname,
      port: targetUrl.port || (targetUrl.protocol === 'https:' ? 443 : 80),
      path: targetUrl.pathname,
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Content-Length': postData.length,
      },
      agent: httpAgent,
      timeout: timeoutMs,
    };

    const startTime = process.hrtime.bigint();

    const req = transport.request(reqOpts, (res) => {
      let body = '';
      res.on('data', (chunk) => (body += chunk));
      res.on('end', () => {
        const endTime = process.hrtime.bigint();
        const latencyMs = Number(endTime - startTime) / 1e6;

        let verdict = 'Unknown';
        if (res.statusCode === 200 || res.statusCode === 201) {
          try {
            const parsed = JSON.parse(body);
            verdict = parsed.status ? parsed.status.description : 'Accepted';
          } catch (e) {
            verdict = 'Malformed JSON';
          }
        }

        resolve({
          statusCode: res.statusCode,
          verdict,
          latencyMs,
          error: null,
        });
      });
    });

    req.on('timeout', () => {
      req.destroy(new Error('ETIMEDOUT'));
    });

    req.on('error', (err) => {
      const endTime = process.hrtime.bigint();
      const latencyMs = Number(endTime - startTime) / 1e6;
      resolve({
        statusCode: 0,
        verdict: 'Network Error',
        latencyMs,
        error: err.message,
      });
    });

    req.write(postData);
    req.end();
  });
}

/**
 * Main benchmark execution handler.
 */
async function main() {
  const config = parseArgs();
  const totalSubmissions = config.users * config.submissionsPerUser;

  let isCleanedUp = false;
  const doCleanup = () => {
    if (config.manageDocker && !isCleanedUp) {
      isCleanedUp = true;
      stopDockerProfile(config.profile);
    }
  };

  process.on('SIGINT', () => { doCleanup(); process.exit(130); });
  process.on('SIGTERM', () => { doCleanup(); process.exit(143); });
  process.on('uncaughtException', (err) => {
    console.error('\nUncaught Exception:', err);
    doCleanup();
    process.exit(1);
  });

  try {
    if (config.manageDocker) {
      startDockerProfile(config.profile);
      await waitForServerReady(config.url);
    }

    const targetUrl = new URL(config.url);
    const isHttps = targetUrl.protocol === 'https:';
    const AgentClass = isHttps ? https.Agent : http.Agent;
    const httpAgent = new AgentClass({
      keepAlive: true,
      maxSockets: Math.max(100, config.users * 2),
      maxFreeSockets: 50,
    });

    const progressBar = new ProgressBar({
      totalUsers: config.users,
      totalSubmissions,
      testcasesPerSubmission: config.testcasesPerSubmission,
      profile: config.profile,
      url: config.url,
    });

    const results = [];
    let activeUsers = 0;
    let completedUsers = 0;
    let completedSubmissions = 0;
    let okCount = 0;
    let busyCount = 0;
    let errorCount = 0;

    const startTime = process.hrtime.bigint();

    const updateProgress = (result) => {
      completedSubmissions++;
      results.push(result);

      if (result.statusCode === 200 || result.statusCode === 201) okCount++;
      else if (result.statusCode === 503) busyCount++;
      else errorCount++;

      const now = process.hrtime.bigint();
      const elapsedSec = Number(now - startTime) / 1e9;
      const rps = elapsedSec > 0 ? okCount / elapsedSec : 0;
      const tps = rps * config.testcasesPerSubmission;

      const okLatencies = results.filter((r) => r.statusCode === 200 || r.statusCode === 201).map((r) => r.latencyMs).sort((a, b) => a - b);
      const p50Ms = okLatencies.length ? okLatencies[Math.floor(okLatencies.length * 0.5)] : 0;

      progressBar.update({
        completedUsers,
        activeUsers,
        completedSubmissions,
        okCount,
        busyCount,
        errorCount,
        rps,
        tps,
        p50Ms,
      });
    };

    const userWorkers = Array.from({ length: config.users }, async (_, userIdx) => {
      if (config.rampUpMs > 0 && userIdx > 0) {
        await new Promise((r) => setTimeout(r, (config.rampUpMs / config.users) * userIdx));
      }

      activeUsers++;
      for (let subIdx = 1; subIdx <= config.submissionsPerUser; subIdx++) {
        const payload = getSubmissionPayload(config.tier, config.testcasesPerSubmission, userIdx + 1, subIdx);
        const res = await sendSubmission(config.url, payload, httpAgent, config.timeoutMs);
        updateProgress(res);
      }
      activeUsers--;
      completedUsers++;
    });

    await Promise.all(userWorkers);
    progressBar.finish();

    const endTime = process.hrtime.bigint();
    const totalDurationSec = Number(endTime - startTime) / 1e9;

    const report = generateReport(results, totalDurationSec, config);
    printReportTable(report);

    if (config.jsonOutput) {
      const jsonPath = path.join(process.cwd(), 'stress-results.json');
      fs.writeFileSync(jsonPath, JSON.stringify(report, null, 2));
      console.log(`Report exported to ${jsonPath}\n`);
    }

    httpAgent.destroy();
  } finally {
    doCleanup();
  }
}

if (require.main === module) {
  main().catch((err) => {
    console.error('\nFatal Error:', err);
    process.exit(1);
  });
}
