# citron

A sandboxed code execution service for competitive programming and automated
assessment. It accepts a submission and its testcases in one request, compiles once,
runs every testcase concurrently under enforced resource limits, and returns a result
for each — including the ones after the first failure.

Languages: C, C++, Java, Python. Adding another is a block of configuration.

## API

Submit a source and all its testcases in one request:

```http
POST /submissions
{
  "language_id": 71,
  "source_code": "...",
  "testcases": [{"stdin": "...", "expected_output": "..."}]
}
```

Add `?base64_encoded=true` to send and receive base64 payloads. Identical sources are
compiled once and served from a content-addressed compile cache.

Also: `GET /languages`, `/health`, `/ready`, `/metrics`.

## Running

```sh
docker compose up
```

The container needs `cap_add: SYS_ADMIN`, `apparmor=unconfined` and
`systempaths=unconfined` for nsjail to create its namespaces. It does not need
`privileged` or the Docker socket.

For development without nsjail, set `sandbox.driver = "local"` and
`sandbox.allow_unsafe_local = true`, then run `make build && ./bin/citron`. The local
driver provides no isolation.

## Scaling

Replicas share no state; run more of them behind a least-connections load balancer.
[examples/](examples/) covers Docker Compose, Swarm, Kubernetes and ECS.

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

Toolchains are probed at startup. With `languages.require_toolchains` set, a missing
one stops the service from starting.

## Security

This service executes untrusted code.

- Bind it to an internal network. Never publish the port on a public interface.
  `server.auth_token` adds a shared-secret header.
- Submissions have no network access, including loopback.
- Each testcase runs in a fresh writable workspace. Compiled artifacts are shared;
  mutable state never is.
- CPU, wall clock, memory, process count, file size and output are all bounded.
  Memory and process limits are enforced by cgroups: v2 where available, v1 as a
  fallback, chosen automatically at startup.

`make security` runs the containment suite against a running instance.

## Configuration

[configs/citron.conf](configs/citron.conf) holds every operational setting: limits,
concurrency, sandbox paths and logging. Every key is optional and falls back to the
value shown there, so a deployment's config only needs what it changes.

## Make targets

| Target | Purpose |
|---|---|
| `build` | Build `bin/citron` |
| `test` / `test-race` | Unit tests |
| `lint` | `go vet` and `gofmt` |
| `up` / `down` | Start and stop with Compose |
| `security` | Containment suite, against a running instance |
| `smoke` | Start the service and run the end-to-end smoke test |
| `stress` | Load test; see `node bench/stress.js --help` |
| `image` | Build the container image |

## Layout

```
cmd/citron            entry point; wires dependencies
internal/judge        domain model, standard library only
internal/config       configuration loading and validation
internal/lang         language registry and manifests
internal/lang/hooks   per-language code, one package each
internal/sandbox      sandbox interface, nsjail driver, cgroup control
internal/run          compile-once pipeline and compile cache
internal/sched        admission control and scheduling
internal/compare      output comparison
internal/api          HTTP handlers
internal/metrics      Prometheus instrumentation
configs               service config and language definitions
examples              API usage and deployment
tests                 security and smoke suites, run against a live service
bench                 load test harness and its deployment profiles
```
