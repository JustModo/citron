# Measured results

Host: 16 cores / 31 GB, Docker 29.7.2, kernel 7.1.8, nsjail 3.6, full isolation on.
The container is CPU- and memory-limited to model each target, so the 2 vCPU rows are
representative of the production box; the larger rows show how it scales.

Reproduce with `docker compose up` and the load harness described at the end.

## Single submission latency, cold compile

| Language | 3 testcases | Compile | Peak memory |
|---|---|---|---|
| Python | 0.05 s | 20 ms | 5.3 MB |
| C | 0.04 s | 25 ms | 2.1 MB |
| C++ | 0.23 s | 218 ms | 2.2 MB |
| Java | 0.40 s | 321 ms | 31.4 MB |

Memory is `memory.peak` from the execution's own cgroup, so it counts pages actually
touched. Java's 31 MB against a 256 MB limit is the JVM's floor, and the reason its
manifest adds headroom.

## 13 testcases via the Judge0-compatible path

The shape the existing consumer sends today, unchanged. The Judge0 baseline it
replaces was roughly 30 s for the same work.

| Language | Total | Per testcase | Compilations |
|---|---|---|---|
| Python | 0.23 s | 18 ms | 1 (12 cache hits) |
| Java | 0.86 s | 66 ms | 1 (13 cache hits) |

The compile cache is what makes this work — without it the same pattern recompiles
identical source once per testcase. The same work as one native batch request is
0.29 s for Java, roughly 3× faster again, because it is one round trip instead of 13.

## Throughput and scaling

5 testcases per submission, sustained load, measured to saturation.

| Config | Python sub/s | Python tc/s | Java sub/s | Java tc/s |
|---|---|---|---|---|
| **2 vCPU**, slots=2, subs=2 | 26 | 130 | 7.5 | 38 |
| **8 vCPU**, slots=8, subs=8 | 61 | 305 | 15.6 | 78 |
| **14 vCPU**, slots=16, subs=16 | 73 | 368 | 20 | 100 |

Scaling is real but sub-linear: 4× the CPUs buys about 2.3×. Each execution pays a
fixed cost — nsjail startup, cgroup creation, workspace setup — that does not shrink
with more cores. Past roughly 8 vCPU the returns fall off sharply, and at 400
concurrent on 14 vCPU throughput *declines* (59 sub/s, down from 73) as contention
costs more than the added parallelism buys.

**Practical guidance:** raise `execution_slots` and `max_concurrent_submissions`
together, to about the core count. Going beyond that buys latency, not throughput.

## Latency under concurrent users, 2 vCPU baseline

5 testcases per submission, Python.

| Concurrent users | p50 | p95 | p99 |
|---|---|---|---|
| 1 | 0.04 s | 0.05 s | 0.10 s |
| 10 | 0.37 s | 0.43 s | 0.45 s |
| 50 | 1.82 s | 2.20 s | 2.21 s |
| 100 | 3.66 s | 4.26 s | 4.27 s |

Java at 100 concurrent is p99 13.4 s — still inside the client's 45 s abort, but it
is the language that will hit the ceiling first.

Throughput is flat from concurrency 5 upward while latency grows linearly. That is
clean saturation: the judge is doing all the work it can and queueing the rest, not
thrashing.

## Overload behaviour

10 testcases per submission on 2 vCPU — deliberately past what the box can serve.

| Concurrent | Completed | Shed (503) | Failed | p99 |
|---|---|---|---|---|
| 200 | 296 | 22 | 0 | 15.0 s |
| 400 | 339 | 199 | 0 | 15.1 s |
| 800 | 343 | 595 | 0 | 15.2 s |

Latency stays flat and nothing fails: excess load is refused quickly with 503 rather
than queued. `judge_queued_submissions` and the 503 rate are the metrics to alert on.

This is the behaviour after adding `scheduler.max_queue_wait_seconds`. The load test
is what exposed its absence — before the fix, the same 800-user run produced **375
client timeouts**, a p99 of 45 s pinned at the client's abort, and throughput
*falling* to 10.2 sub/s because the judge kept working on requests nobody was waiting
for any more. Bounding the queue wait turned that into 13.6 sub/s and zero failures.
Shedding load is faster than absorbing it.

## Limits

On the 2 vCPU / 8 GB target, with 5-testcase submissions:

- **Comfortable:** ~100 concurrent users, p99 under 5 s.
- **Acceptable:** ~200 concurrent, p99 around 15 s.
- **Shedding:** beyond ~250 concurrent the judge starts returning 503s. It stays
  responsive and never fails; some users are asked to retry.

Java roughly halves each of those figures. Sizing for a Java-heavy assessment means
either fewer concurrent users or more cores.

## Harness

Concurrent virtual users, each submitting repeatedly for a fixed duration, counting
successes, 503s and failures separately — a 503 under overload is correct behaviour
and must not be scored as an error. Latency percentiles cover successful submissions
only. Source distinct per virtual user so the compile cache does not mask compile cost.

## Not measured

Sustained multi-hour load, and behaviour under memory pressure from many concurrent
Java submissions specifically. Both are worth checking before a large assessment.
