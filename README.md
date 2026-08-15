# citron

A sandboxed code execution service for competitive programming and automated
assessment. It accepts a submission and its testcases in one request, compiles once,
runs every testcase concurrently under enforced resource limits, and returns a result
for each — including the ones after the first failure.

Languages: C, C++, Java, Python. Adding another is a block of configuration.

## API

Submit a whole submission at once:

```http
POST /submissions
{
  "language_id": 71,
  "source_code": "...",
  "testcases": [{"stdin": "...", "expected_output": "..."}]
}
```

A legacy single-testcase form is also accepted, for clients that send one request per
testcase. It runs the same pipeline, and a content-addressed compile cache means such
a client still compiles each submission only once.

```http
POST /submissions?base64_encoded=true&wait=true
{"source_code": "<b64>", "language_id": 71, "stdin": "<b64>", "expected_output": "<b64>"}
```

Also: `GET /languages`, `/health`, `/ready`, `/metrics`.

## Running

```sh
docker compose up
```

The container needs `cap_add: SYS_ADMIN`, `apparmor=unconfined` and
`systempaths=unconfined` so nsjail can build its namespaces. It never runs
`privileged`, and no Docker socket is mounted. The reasoning is in
[docs/sandbox.md](docs/sandbox.md).

For development on a machine without nsjail:

```sh
make build && ./bin/citron -config configs/citron.conf
```

That requires `sandbox.driver = "local"` and `sandbox.allow_unsafe_local = true`. The
local driver does not isolate anything; the service refuses to start otherwise.

## Adding a language

Add a block to [configs/languages.toml](configs/languages.toml) and install the
toolchain in the image. No code:

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

A language needing behaviour a template cannot express — Java must name its file after
the public class — sets `hook` and implements it in its own package under
[internal/lang/hooks/](internal/lang/hooks/).

Every configured toolchain is probed at startup, and a missing one is a startup
failure rather than a confusing verdict later.

## Security

This service executes untrusted code.

- Bind it to an internal network. Never publish the port on a public interface.
  `server.auth_token` adds a shared-secret header.
- Submissions get no network: no internet, no DNS, no loopback, no cloud metadata,
  no route back to this service.
- Each testcase runs in a fresh writable workspace. Compiled artifacts are shared;
  mutable state never is.
- CPU, wall clock, memory, process count, file size and output are all bounded.
  Memory and process limits are enforced by cgroups v2.

`make security` runs the containment suite against a running instance.

## Configuration

[configs/citron.conf](configs/citron.conf) holds every operational setting: limits,
concurrency, sandbox paths and logging. Nothing important is hardcoded.

Scale by raising `execution_slots` and `max_concurrent_submissions` towards the core
count. See [docs/benchmarks.md](docs/benchmarks.md) for measured throughput and the
point where returns stop.

## Make targets

| Target | Purpose |
|---|---|
| `build` | Build `bin/citron` |
| `test` / `test-race` | Unit tests |
| `lint` | `go vet` and `gofmt` |
| `up` / `down` | Start and stop with Compose |
| `integration` | Tests requiring real toolchains |
| `security` | Containment suite, against a running instance |
| `spike` | Verify nsjail and cgroup v2 work on this host |
| `image` | Build the container image |

## Layout

```
cmd/citron         composition root; dependencies are built and injected here
internal/judge     domain model, standard library only
internal/config    configuration loading and validation
internal/lang      language registry and manifests
internal/lang/hooks  per-language code, one package each
internal/sandbox   sandbox interface, nsjail driver, cgroup control
internal/run       compile-once pipeline and compile cache
internal/sched     admission control and scheduling
internal/compare   output comparison
internal/api       HTTP handlers
internal/metrics   Prometheus instrumentation
```
