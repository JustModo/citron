#!/usr/bin/env python3
"""Load harness for the judge.

Sends realistic assessment traffic: one submission per virtual user, repeatedly,
for a fixed duration. Reports throughput and latency percentiles, and separates
"the judge said no" (503 backpressure, which is correct under overload) from
"the judge broke" (5xx, timeouts).
"""
import argparse, json, statistics, sys, threading, time, urllib.error, urllib.request

PROGRAMS = {
    "python": (71, "import sys\na,b=map(int,sys.stdin.read().split())\nprint(a+b)"),
    "c": (50, '#include <stdio.h>\nint main(){int a,b;scanf("%d %d",&a,&b);printf("%d\\n",a+b);return 0;}'),
    "java": (62, "import java.util.Scanner;\npublic class Main{public static void main(String[] a){"
                 "Scanner s=new Scanner(System.in);System.out.println(s.nextInt()+s.nextInt());}}"),
}


def submit(url, lang_id, source, ntests, timeout):
    cases = [{"stdin": f"{i} {i}", "expected_output": str(2 * i)} for i in range(1, ntests + 1)]
    body = json.dumps({"language_id": lang_id, "source_code": source, "testcases": cases}).encode()
    req = urllib.request.Request(url + "/submissions", data=body,
                                 headers={"Content-Type": "application/json"})
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            res = json.loads(r.read())
        dt = time.perf_counter() - t0
        accepted = res["status"]["description"] == "Accepted"
        return ("ok" if accepted else "verdict:" + res["status"]["description"], dt)
    except urllib.error.HTTPError as e:
        dt = time.perf_counter() - t0
        return ("busy" if e.code == 503 else f"http{e.code}", dt)
    except Exception as e:
        return (type(e).__name__, time.perf_counter() - t0)


def run(url, concurrency, duration, lang, ntests, timeout):
    lang_id, source = PROGRAMS[lang]
    # Vary the source per worker so the compile cache does not hide compile cost
    # entirely; each virtual user is a different "student".
    results, lock, stop = [], threading.Lock(), threading.Event()

    def worker(idx):
        src = source + f"\n# user {idx}\n" if lang == "python" else source
        while not stop.is_set():
            outcome = submit(url, lang_id, src, ntests, timeout)
            with lock:
                results.append(outcome)

    threads = [threading.Thread(target=worker, args=(i,), daemon=True) for i in range(concurrency)]
    t0 = time.perf_counter()
    for t in threads:
        t.start()
    time.sleep(duration)
    stop.set()
    for t in threads:
        t.join(timeout=timeout + 5)
    elapsed = time.perf_counter() - t0

    ok = [d for s, d in results if s == "ok"]
    busy = [d for s, d in results if s == "busy"]
    bad = [(s, d) for s, d in results if s not in ("ok", "busy")]
    lat = sorted(ok)

    def pct(p):
        return lat[min(int(len(lat) * p), len(lat) - 1)] if lat else float("nan")

    return {
        "concurrency": concurrency,
        "completed": len(results),
        "ok": len(ok),
        "busy": len(busy),
        "errors": len(bad),
        "error_kinds": sorted({s for s, _ in bad}),
        "rps": len(ok) / elapsed,
        "testcases_per_sec": len(ok) * ntests / elapsed,
        "p50": pct(0.50), "p95": pct(0.95), "p99": pct(0.99),
        "max": max(lat) if lat else float("nan"),
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:2358")
    ap.add_argument("--concurrency", type=int, nargs="+", default=[1, 10, 50, 100, 200])
    ap.add_argument("--duration", type=float, default=10)
    ap.add_argument("--lang", default="python", choices=list(PROGRAMS))
    ap.add_argument("--testcases", type=int, default=5)
    ap.add_argument("--timeout", type=float, default=45)
    ap.add_argument("--label", default="")
    args = ap.parse_args()

    print(f"\n=== {args.label or args.lang} | {args.testcases} testcases/submission "
          f"| {args.duration:.0f}s per step ===")
    print(f"{'conc':>5} {'ok':>6} {'busy':>6} {'err':>5} {'sub/s':>7} {'tc/s':>7} "
          f"{'p50':>7} {'p95':>7} {'p99':>7} {'max':>7}")
    for c in args.concurrency:
        r = run(args.url, c, args.duration, args.lang, args.testcases, args.timeout)
        print(f"{r['concurrency']:>5} {r['ok']:>6} {r['busy']:>6} {r['errors']:>5} "
              f"{r['rps']:>7.1f} {r['testcases_per_sec']:>7.1f} "
              f"{r['p50']:>7.3f} {r['p95']:>7.3f} {r['p99']:>7.3f} {r['max']:>7.3f}",
              flush=True)
        if r["error_kinds"]:
            print(f"      errors: {r['error_kinds']}", flush=True)
        time.sleep(2)


if __name__ == "__main__":
    main()
