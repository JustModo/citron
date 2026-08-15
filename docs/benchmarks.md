# Measured results

Development machine (16 cores / 31 GB), Docker 29.7.2, kernel 7.1.8, nsjail 3.6,
full isolation enabled. The production target is 2 vCPU / 8 GB, so treat these as
relative improvements rather than absolute numbers — the ratios are what transfer.

Reproduce with `docker compose up` and the requests in this file.

## 13 testcases, one HTTP request per testcase

This is the shape the existing consumer sends today, unchanged. The Judge0 baseline
it replaces was roughly 30 s for the same work.

| Language | Total | Per testcase | Compilations |
|---|---|---|---|
| Python | 0.23 s | 18 ms | 1 (12 cache hits) |
| Java | 0.86 s | 66 ms | 1 (13 cache hits) |

The compile cache is what makes this work. Without it the same request pattern
recompiles the identical source once per testcase, which is exactly the ~1 s × N that
made the Judge0 setup slow. `judge_compiles_total` shows the split:

```
judge_compiles_total{language="java",outcome="success"} 1
judge_compiles_total{language="java",outcome="cached"}  13
```

## 13 testcases, one batch request

The native API, once a client is migrated to it.

| Language | Total | Compile |
|---|---|---|
| Java | 0.29 s | 371 ms (cached on repeat) |

Roughly 3× faster again than the per-testcase path, because there is one HTTP
round trip instead of thirteen.

## Single submission, 3 testcases, cold compile

| Language | Total | Compile | Peak memory |
|---|---|---|---|
| Python | 0.05 s | 20 ms | 5.3 MB |
| C | 0.04 s | 25 ms | 2.1 MB |
| C++ | 0.23 s | 218 ms | 2.2 MB |
| Java | 0.40 s | 321 ms | 31.4 MB |

Memory comes from the execution's own cgroup (`memory.peak`), so it counts pages the
submission actually touched. Java's 31 MB against a 256 MB limit is the JVM's floor,
and the reason its manifest adds headroom: capping it at a bare 256 MB would leave
roughly 90 MB of usable heap once non-heap overhead is paid.

## Containment

`make security` — 16 checks, all passing under nsjail:

| Attack | Outcome |
|---|---|
| Fork bomb | Time Limit Exceeded, 6.5 s, no survivors |
| Memory bomb | Memory Limit Exceeded at 288 MB against a 256 MB cap |
| CPU loop | Time Limit Exceeded |
| Output bomb | Output Limit Exceeded, exactly 1 MB captured |
| File bomb | contained by the sized tmpfs |
| Network (internet, DNS, metadata, loopback) | all refused |
| Reading /etc/shadow, judge.conf, the compile cache | all refused |
| Path traversal, writing to the image, mount(), privilege | all refused |
| Orphaned descendants | none survive |

The suite is checked against a **deliberately weakened** judge as well: run the same
tests with `sandbox.driver = "local"` and 17 of them fail. A security suite that has
never been seen to fail is not evidence of anything.

## What has not been measured

Load and concurrency behaviour on the 2 vCPU target — the numbers above are all
single-submission latency on a much larger machine. Before production, run the same
requests at 1/10/50/100/200 concurrent and confirm that overload degrades into 503s
and queue waits rather than timeouts, and that p99 stays under the client's 45 s
abort. `scheduler.execution_slots` and `limits.submission.max_parallel_testcases`
should be tuned from those measurements, not from these.
