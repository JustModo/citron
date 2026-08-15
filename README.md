# judge

A code-execution judge for competitive-programming assessments. Written in Go as a
faster, smaller, more controllable replacement for Judge0.

Accepts a submission plus N testcases in one request, compiles **once**, runs the
testcases concurrently under bounded resources inside nsjail, and returns a result for
every testcase — including the ones after the first failure.

Languages: C, C++, Java, Python. Adding one is a block in `configs/languages.toml`.

## Status

Under construction. See [DESIGN.md](DESIGN.md) for the architecture.

## Running locally

Development, on a host without nsjail (the sandbox is **not** enforced — dev only):

```sh
make build && ./bin/judge -config configs/judge.conf
```

Real sandboxing requires the worker image:

```sh
make spike          # verifies nsjail + cgroup v2 work on this host
docker compose -f deployments/docker-compose.yml up
```

## Security

This service executes untrusted code. Never publish its port on `0.0.0.0` — bind it to
an internal network. The worker container needs `cap_add: SYS_ADMIN` for namespace
creation; it must never run `--privileged` and never has access to the Docker socket.

## Make targets

| Target | What it does |
|---|---|
| `build` | Build `bin/judge` |
| `test` / `test-race` | Unit tests |
| `integration` | Tests needing real toolchains |
| `security` | Sandbox escape and resource-bomb suite |
| `spike` | Phase-0 sandbox capability check |
| `bench` | Benchmarks |
