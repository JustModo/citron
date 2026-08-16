/**
 * Terminal progress dashboard for citron load testing.
 */

class ProgressBar {
  /**
   * @param {Object} opts
   * @param {number} opts.totalUsers
   * @param {number} opts.totalSubmissions
   * @param {number} opts.testcasesPerSubmission
   * @param {string} opts.profile
   * @param {string} opts.url
   */
  constructor({ totalUsers, totalSubmissions, testcasesPerSubmission, profile, url }) {
    this.totalUsers = totalUsers;
    this.totalSubmissions = totalSubmissions;
    this.testcasesPerSubmission = testcasesPerSubmission;
    this.profile = profile;
    this.url = url;
    this.isTTY = process.stdout.isTTY;
    this.lastRenderTime = 0;

    this.initHeader();
  }

  /**
   * Prints the benchmark session header banner.
   */
  initHeader() {
    console.log(`\n================================================================================`);
    console.log(`  citron Load Harness`);
    console.log(`  Target: ${this.url} | Profile: ${this.profile.toUpperCase()}`);
    console.log(`  Concurrency: ${this.totalUsers} Users | Total Submissions: ${this.totalSubmissions}`);
    console.log(`  Batching: ${this.testcasesPerSubmission} Testcases/Submission (${(this.totalSubmissions * this.testcasesPerSubmission).toLocaleString()} Total Testcases)`);
    console.log(`================================================================================\n`);
  }

  /**
   * Renders real-time progress bar and metrics to stdout.
   * @param {Object} stats
   * @param {number} [stats.activeUsers]
   * @param {number} [stats.completedSubmissions]
   * @param {number} [stats.okCount]
   * @param {number} [stats.busyCount]
   * @param {number} [stats.errorCount]
   * @param {number} [stats.rps]
   * @param {number} [stats.tps]
   * @param {number} [stats.p50Ms]
   */
  update(stats) {
    const now = Date.now();
    if (this.isTTY && now - this.lastRenderTime < 50 && stats.completedSubmissions < this.totalSubmissions) {
      return;
    }
    this.lastRenderTime = now;

    const {
      activeUsers = 0,
      completedSubmissions = 0,
      okCount = 0,
      busyCount = 0,
      errorCount = 0,
      rps = 0,
      tps = 0,
      p50Ms = 0,
    } = stats;

    const ratio = Math.min(1, Math.max(0, completedSubmissions / this.totalSubmissions));
    const percent = Math.floor(ratio * 100);

    const barWidth = 30;
    const filledLen = Math.floor(barWidth * ratio);
    const emptyLen = barWidth - filledLen;
    const barStr = '█'.repeat(filledLen) + '░'.repeat(emptyLen);

    const line1 = `Progress: [${barStr}] ${percent.toString().padStart(3)}% | ${completedSubmissions}/${this.totalSubmissions} Submissions (${(completedSubmissions * this.testcasesPerSubmission).toLocaleString()} TCs) | Active Users: ${activeUsers}`;
    const line2 = `Metrics:  ${rps.toFixed(1)} sub/s | ${tps.toFixed(0)} tc/s | OK: ${okCount} | 503 Busy: ${busyCount} | Errors: ${errorCount} | Latency p50: ${p50Ms > 0 ? p50Ms.toFixed(0) + 'ms' : 'N/A'}`;

    if (this.isTTY) {
      process.stdout.write(`\r\x1b[K${line1}\n\r\x1b[K${line2}\x1b[1A`);
    } else {
      if (percent % 10 === 0 && percent !== this.lastPercent) {
        console.log(`[${percent}%] ${completedSubmissions}/${this.totalSubmissions} subs | ${rps.toFixed(1)} sub/s | OK: ${okCount} | Busy: ${busyCount} | Err: ${errorCount}`);
        this.lastPercent = percent;
      }
    }
  }

  /**
   * Finalizes progress output.
   */
  finish() {
    if (this.isTTY) {
      process.stdout.write(`\n\n`);
    } else {
      console.log(`\nRun complete.\n`);
    }
  }
}

module.exports = { ProgressBar };
