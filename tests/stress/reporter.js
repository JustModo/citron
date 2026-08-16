/**
 * Metrics aggregation and summary reporting for citron load tests.
 */

/**
 * Calculates a specific percentile value from a sorted numbers array.
 * @param {number[]} sortedArray
 * @param {number} percentile
 * @returns {number}
 */
function calculatePercentile(sortedArray, percentile) {
  if (!sortedArray || sortedArray.length === 0) return 0;
  const index = Math.min(
    sortedArray.length - 1,
    Math.max(0, Math.floor(sortedArray.length * (percentile / 100)))
  );
  return sortedArray[index];
}

/**
 * Aggregates raw stress test results into summary stats and percentiles.
 * @param {Array<Object>} results
 * @param {number} durationSec
 * @param {Object} config
 * @returns {Object} Report summary object
 */
function generateReport(results, durationSec, config) {
  const totalSubmissions = results.length;
  const totalTestcases = totalSubmissions * (config.testcasesPerSubmission || 100);

  const statusCounts = {};
  const verdictCounts = {};
  const latencies = [];

  let okCount = 0;
  let busyCount = 0;
  let errorCount = 0;

  for (const res of results) {
    const code = res.statusCode || 'Error';
    statusCounts[code] = (statusCounts[code] || 0) + 1;

    if (code === 200 || code === 201) {
      okCount++;
      if (res.latencyMs) latencies.push(res.latencyMs);

      const verdict = res.verdict || 'Unknown';
      verdictCounts[verdict] = (verdictCounts[verdict] || 0) + 1;
    } else if (code === 503) {
      busyCount++;
    } else {
      errorCount++;
    }
  }

  latencies.sort((a, b) => a - b);

  const avgLatency = latencies.length ? latencies.reduce((a, b) => a + b, 0) / latencies.length : 0;
  const minLatency = latencies.length ? latencies[0] : 0;
  const maxLatency = latencies.length ? latencies[latencies.length - 1] : 0;
  const p50 = calculatePercentile(latencies, 50);
  const p90 = calculatePercentile(latencies, 90);
  const p95 = calculatePercentile(latencies, 95);
  const p99 = calculatePercentile(latencies, 99);

  const rps = durationSec > 0 ? okCount / durationSec : 0;
  const tps = durationSec > 0 ? (okCount * (config.testcasesPerSubmission || 100)) / durationSec : 0;

  return {
    config: {
      profile: config.profile,
      url: config.url,
      users: config.users,
      submissionsPerUser: config.submissionsPerUser,
      testcasesPerSubmission: config.testcasesPerSubmission,
    },
    summary: {
      totalDurationSec: durationSec,
      totalSubmissions,
      totalTestcases,
      successfulSubmissions: okCount,
      busySheddingSubmissions: busyCount,
      failedSubmissions: errorCount,
      throughputSubmissionsPerSec: rps,
      throughputTestcasesPerSec: tps,
    },
    latencyMs: {
      min: minLatency,
      avg: avgLatency,
      p50,
      p90,
      p95,
      p99,
      max: maxLatency,
    },
    httpStatuses: statusCounts,
    verdicts: verdictCounts,
  };
}

/**
 * Prints formatted benchmark summary report table to stdout.
 * @param {Object} report
 */
function printReportTable(report) {
  const { config, summary, latencyMs, httpStatuses, verdicts } = report;

  console.log(`\n================================================================================`);
  console.log(`                       citron LOAD TEST SUMMARY REPORT                          `);
  console.log(`================================================================================`);
  console.log(`  Target URL:       ${config.url}`);
  console.log(`  Target Profile:   ${config.profile.toUpperCase()}`);
  console.log(`  Virtual Users:    ${config.users}`);
  console.log(`  Submissions/User: ${config.submissionsPerUser}`);
  console.log(`  Testcases/Batch:  ${config.testcasesPerSubmission}`);
  console.log(`  Elapsed Time:     ${summary.totalDurationSec.toFixed(2)}s`);
  console.log(`--------------------------------------------------------------------------------`);
  console.log(`  THROUGHPUT:`);
  console.log(`    Successful Submissions / sec (RPS):  ${summary.throughputSubmissionsPerSec.toFixed(2)} sub/s`);
  console.log(`    Executed Testcases / sec (TPS):     ${summary.throughputTestcasesPerSec.toFixed(1)} tc/s`);
  console.log(`    Total Testcases Executed:           ${(summary.successfulSubmissions * config.testcasesPerSubmission).toLocaleString()}`);
  console.log(`--------------------------------------------------------------------------------`);
  console.log(`  LATENCY (201/200 OK):`);
  console.log(`    Min:  ${latencyMs.min.toFixed(1).padStart(7)} ms | P50: ${latencyMs.p50.toFixed(1).padStart(7)} ms | P95: ${latencyMs.p95.toFixed(1).padStart(7)} ms`);
  console.log(`    Avg:  ${latencyMs.avg.toFixed(1).padStart(7)} ms | P90: ${latencyMs.p90.toFixed(1).padStart(7)} ms | P99: ${latencyMs.p99.toFixed(1).padStart(7)} ms`);
  console.log(`    Max:  ${latencyMs.max.toFixed(1).padStart(7)} ms`);
  console.log(`--------------------------------------------------------------------------------`);
  console.log(`  HTTP STATUS CODES:`);
  for (const [status, count] of Object.entries(httpStatuses)) {
    const label = (status === '201' || status === '200')
      ? `${status} Created/OK`
      : status === '503'
      ? '503 Service Unavailable (Overload Shedding)'
      : `HTTP ${status}`;
    const pct = ((count / summary.totalSubmissions) * 100).toFixed(1);
    console.log(`    ${label.padEnd(48)}: ${count.toString().padStart(6)} (${pct}%)`);
  }
  console.log(`--------------------------------------------------------------------------------`);
  console.log(`  EXECUTION VERDICTS:`);
  for (const [verdict, count] of Object.entries(verdicts)) {
    const pct = summary.successfulSubmissions > 0 ? ((count / summary.successfulSubmissions) * 100).toFixed(1) : '0.0';
    console.log(`    ${verdict.padEnd(48)}: ${count.toString().padStart(6)} (${pct}%)`);
  }
  console.log(`================================================================================\n`);
}

module.exports = {
  generateReport,
  printReportTable,
};
