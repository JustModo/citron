# Getting started

Start an instance with `make up`; it listens on `127.0.0.1:2358`.

## Submit a solution

One request carries the source and every testcase. The source is compiled once and
each testcase runs in its own sandbox.

```sh
curl -s localhost:2358/submissions -H 'Content-Type: application/json' -d @examples/getting-started/sum.json
```

The response has an overall `status` and one result per testcase:

```json
{
  "id": "5f0c…",
  "status": {"id": 3, "description": "Accepted"},
  "compile": {"skipped": false, "success": true, "duration_ms": 0, "cached": false},
  "wall_time_ms": 48,
  "testcases": [
    {"index": 0, "status": {"id": 3, "description": "Accepted"}, "stdout": "5\n",
     "exit_code": 0, "cpu_time_ms": 12, "wall_time_ms": 21, "memory_kb": 4256, "memory_source": "cgroup"}
  ]
}
```

Status ids are stable: `3` Accepted, `4` Wrong Answer, `5` Time Limit Exceeded,
`6` Compilation Error, `7`–`12` runtime errors and memory or output limits, `13` System Error.

## Tighter limits

A request may lower the configured limits, never raise them. `memory_limit` is in KB.

```sh
curl -s localhost:2358/submissions -H 'Content-Type: application/json' -d '{
  "language": "cpp",
  "source_code": "#include <cstdio>\nint main(){long a,b;scanf(\"%ld %ld\",&a,&b);printf(\"%ld\\n\",a+b);}",
  "cpu_time_limit": 1,
  "memory_limit": 65536,
  "testcases": [{"stdin": "2 3", "expected_output": "5"}]
}'
```

## Base64 payloads

For sources or outputs that are not valid UTF-8, add `?base64_encoded=true`. Every
source, stdin, expected output and returned stream is then base64.

```sh
curl -s 'localhost:2358/submissions?base64_encoded=true' -H 'Content-Type: application/json' -d "{
  \"language_id\": 71,
  \"source_code\": \"$(printf 'print(input())' | base64 -w0)\",
  \"testcases\": [{\"stdin\": \"$(printf 'hi' | base64 -w0)\", \"expected_output\": \"$(printf 'hi' | base64 -w0)\"}]
}"
```

## Authentication

With `server.auth_token` set, every endpoint except `/health` requires the token:

```sh
curl -s localhost:2358/languages -H 'X-Judge-Token: <token>'
```

## Other endpoints

| Endpoint | Returns |
|---|---|
| `GET /languages` | Configured languages and their ids |
| `GET /health` | `200` while the process is alive |
| `GET /ready` | `200` when accepting work, `503` when shutting down |
| `GET /metrics` | Prometheus metrics |
