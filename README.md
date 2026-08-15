# judge

A code-execution judge for competitive-programming assessments. Written in Go as a
faster, smaller, more controllable replacement for Judge0.

It takes a submission plus N testcases in one request, compiles **once**, runs the
testcases concurrently under bounded resources inside nsjail, and returns a result for
every testcase — including the ones after the first failure.

One container replaces Judge0's four, and it does not run privileged.

Languages: C, C++, Java, Python. Adding one is a block in
[configs/languages.toml](configs/languages.toml).

## Two APIs

The native API takes a whole submission at once:

```http
POST /submissions
{"language_id": 71, "source_code": "...", "testcases": [{"stdin": "...", "expected_output": "..."}]}
```

The Judge0-compatible endpoint accepts the single-testcase form, so an existing
Judge0 client migrates by changing a URL:

```http
POST /submissions?base64_encoded=true&wait=true
{"source_code": "<b64>", "language_id": 71, "stdin": "<b64>", "expected_output": "<b64>"}
```

Both run the same pipeline. A content-addressed compile cache means the
one-request-per-testcase client also compiles only once, so it gets most of the
speedup without changing anything: 13 Java testcases in 0.86 s against a ~30 s
Judge0 baseline. See [docs/benchmarks.md](docs/benchmarks.md).

Also: `GET /languages`, `/health`, `/ready`, `/metrics`.

## Running it

```sh
docker compose -f deployments/docker-compose.yml up
```

The container needs `cap_add: SYS_ADMIN`, `apparmor=unconfined` and
`systempaths=unconfined` for nsjail to build its namespaces. Each is explained in
[docs/sandbox-spike.md](docs/sandbox-spike.md); none of them is `privileged: true`,
and the Docker socket is never mounted.

On a development machine without nsjail:

```sh
make build && ./bin/judge -config configs/judge.conf
```

That requires setting `sandbox.driver = "local"` **and**
`sandbox.allow_unsafe_local = true`, because the local driver does not isolate
anything. The judge refuses to start otherwise.

## Adding a language

Add a block to [configs/languages.toml](configs/languages.toml) and the toolchain to
the worker image. No Go code:

```toml
[[language]]
id = 60
name = "go"
label = "Go"
source = "main.go"
binary = "main"
compile = ["go", "build", "-o", "{{.Binary}}", "{{.Source}}"]
run = ["./{{.Binary}}"]
probe = ["go", "version"]
```

A language needing behaviour a template cannot express — Java has to name its file
after the public class — sets `hook` and implements it in
its own package under [internal/lang/hooks/](internal/lang/hooks/).

Startup probes every configured toolchain and refuses to start if one is missing,
rather than accepting submissions it cannot run.

## Security

This service executes untrusted code.

- **Never publish its port on a public interface.** Put it on an internal network.
  `server.auth_token` adds a shared-secret header if you need one.
- Submissions get no network at all: no internet, no DNS, no loopback, no cloud
  metadata, no reach back to the judge's own API.
- Every testcase gets a fresh writable workspace; artifacts are shared, mutable state
  never is.
- Limits on CPU, wall clock, memory, processes, file size, stdout and stderr are all
  enforced, memory and process count by cgroups v2.

`make security` runs the containment suite against a live judge.

## Make targets

| Target | What it does |
|---|---|
| `build` | Build `bin/judge` |
| `test` / `test-race` | Unit tests |
| `integration` | Tests needing real toolchains |
| `security` | Sandbox escape and resource-bomb suite |
| `spike` | Verify nsjail and cgroup v2 work on this host |
| `worker-image` | Build the container image |

## Layout

```
cmd/judge          composition root: everything is constructed and injected here
internal/judge     domain model, imports nothing but the standard library
internal/config    judge.conf, validated at startup
internal/lang      language registry and manifests
internal/lang/hooks  language-specific Go code, one package each
internal/sandbox   Sandbox interface, nsjail driver, cgroup control, local dev driver
internal/run       compile-once pipeline and the compile cache
internal/sched     memory admission and submission scheduling
internal/compare   output comparison
internal/api       HTTP handlers, native and Judge0-compatible
internal/metrics   Prometheus instrumentation
```

Design rationale is in [DESIGN.md](DESIGN.md); deviations from it and the reasoning
are recorded in [docs/](docs/).
