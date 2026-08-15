# High-Performance Go Code Judge

## Final System Design & Requirements

**Status:** Final architecture baseline
**Implementation language:** Go
**Target languages:** C, C++, Java, Python
**Initial deployment:** Single Linux/AWS EC2 machine
**Minimum baseline:** 2 vCPU / 8 GB RAM
**Primary objective:** Maximum practical execution throughput and low latency while maintaining strong sandbox isolation and operational reliability.

---

# 1. Executive Summary

This project is a custom competitive-programming judge designed as a faster, simpler, and more controllable alternative to Judge0.

The entire application and all orchestration logic will be implemented in **Go** using modern Go engineering practices.

The system will:

* accept source code and multiple testcases in a single request;
* support C, C++, Java, and Python;
* compile C/C++/Java exactly once per submission;
* execute every testcase;
* execute independent testcases concurrently within resource limits;
* return results for every testcase even when earlier tests fail;
* support Judge0-like testcase semantics;
* support Base64-encoded request data;
* support synchronous HTTP responses;
* optionally expose execution events through SSE;
* run submitted programs inside isolated Docker/NSJail environments;
* completely disable network access for submitted programs;
* enforce CPU, wall-clock, memory, process, filesystem, stdout, and stderr limits;
* prevent submitted programs from affecting the judge infrastructure;
* reuse long-lived worker containers instead of creating a container per testcase;
* use Redis Streams for reliable transient job coordination where necessary;
* avoid permanent submission storage;
* recover gracefully from worker/infrastructure failures;
* support more than 100 concurrent users through resource-aware scheduling;
* scale vertically by increasing machine resources without requiring architectural changes.

The design must prioritize **performance without sacrificing isolation or maintainability**.

---

# 2. Core Engineering Requirement

The entire system must be designed as a **proper Go application**, not as a collection of scripts wrapped in Go.

The implementation must follow modern Go best practices around:

* strong typing;
* explicit interfaces;
* dependency injection;
* constructor-based composition;
* separation of concerns;
* package boundaries;
* context propagation;
* structured errors;
* explicit resource ownership;
* deterministic lifecycle management;
* concurrency safety;
* graceful shutdown;
* observability;
* testability;
* dependency inversion;
* minimal global state.

The system must be designed so that individual subsystems can be developed, tested, benchmarked, replaced, and reasoned about independently.

---

# 3. Architectural Philosophy

The architecture should follow:

```text
Domain
  ↓
Application
  ↓
Infrastructure
```

with infrastructure dependencies injected from the composition root.

Business/application logic must not directly depend on:

```text
Docker
Redis
HTTP
NSJail
OS filesystem
os/exec
Prometheus
specific logging implementations
```

Instead, these should be hidden behind interfaces where abstraction is useful.

For example:

```go
type Compiler interface {
    Compile(ctx context.Context, request CompileRequest) (CompileResult, error)
}

type Sandbox interface {
    Execute(ctx context.Context, request SandboxRequest) (SandboxResult, error)
}

type JobQueue interface {
    Enqueue(ctx context.Context, job Job) error
    Consume(ctx context.Context) (Job, error)
    Ack(ctx context.Context, job Job) error
}
```

The application layer should depend on these contracts rather than concrete implementations.

---

# 4. Composition Root

There should be a single explicit application composition root.

Conceptually:

```text
cmd/judge-server/main.go

    ↓

load config
    ↓
initialize logger
    ↓
initialize metrics
    ↓
initialize Redis
    ↓
construct repositories
    ↓
construct queue
    ↓
construct compiler registry
    ↓
construct sandbox
    ↓
construct scheduler
    ↓
construct submission service
    ↓
construct HTTP handlers
    ↓
construct server
    ↓
start application
```

The same principle applies to the worker:

```text
cmd/judge-worker/main.go

    ↓
load config
    ↓
initialize dependencies
    ↓
construct compiler registry
    ↓
construct sandbox
    ↓
construct workspace manager
    ↓
construct execution manager
    ↓
construct worker
    ↓
start worker
```

There should be no hidden dependency initialization throughout the application.

---

# 5. Dependency Injection

Dependency injection should be explicit and idiomatic.

Prefer:

```go
func NewSubmissionService(
    scheduler Scheduler,
    comparator Comparator,
    events EventPublisher,
    logger Logger,
) *SubmissionService
```

over:

```go
func NewSubmissionService() *SubmissionService {
    redis := globalRedisClient
    ...
}
```

No global service locator should exist.

No package should secretly initialize its own Redis connection, logger, database, sandbox, or scheduler.

---

# 6. Interface Design

Interfaces should be defined **at the point of use**, where practical.

Avoid creating interfaces for every struct merely to satisfy an abstract architecture.

Use interfaces where they provide:

* dependency inversion;
* testability;
* multiple implementations;
* meaningful abstraction boundaries.

For example:

```go
type ArtifactStore interface {
    Put(ctx context.Context, artifact Artifact) (ArtifactRef, error)
    Get(ctx context.Context, ref ArtifactRef) (Artifact, error)
    Delete(ctx context.Context, ref ArtifactRef) error
}
```

The concrete filesystem implementation belongs in infrastructure.

---

# 7. Strong Typing

The system should use domain-specific types rather than passing primitive values everywhere.

Avoid:

```go
func Execute(
    language string,
    memory int64,
    timeout int64,
)
```

Prefer:

```go
type LanguageID int
type MemoryBytes int64
type Duration time.Duration
type TestCaseIndex int
type SubmissionID string
type JobID string
```

Resource limits should be represented by explicit structures:

```go
type ResourceLimits struct {
    CPUTime       time.Duration
    WallTime      time.Duration
    Memory        MemoryBytes
    Stack         MemoryBytes
    MaxProcesses  int
    MaxFileSize   int64
    MaxStdout     int64
    MaxStderr     int64
}
```

This reduces unit mistakes and makes APIs self-documenting.

---

# 8. Separation of Concerns

Every major responsibility must be isolated.

The system should have distinct modules for:

```text
API
Submission application logic
Scheduling
Queueing
Compilation
Execution
Sandboxing
Workspace management
Resource management
Comparison
Language definitions
Artifact storage
Event publishing
Worker management
Configuration
Observability
Health
```

No single module should become a "god service."

For example:

```text
ExecutionManager
```

should not also:

* parse HTTP;
* talk directly to Redis;
* compare outputs;
* manage Docker;
* write Prometheus metrics;
* parse configuration.

Each responsibility belongs to its appropriate module.

---

# 9. Recommended Go Project Structure

```text
judge/
│
├── cmd/
│   ├── judge-server/
│   │   └── main.go
│   │
│   └── judge-worker/
│       └── main.go
│
├── internal/
│   │
│   ├── domain/
│   │   ├── submission/
│   │   ├── testcase/
│   │   ├── execution/
│   │   ├── language/
│   │   ├── artifact/
│   │   └── result/
│   │
│   ├── application/
│   │   ├── submission/
│   │   ├── execution/
│   │   ├── scheduling/
│   │   └── worker/
│   │
│   ├── api/
│   │   ├── http/
│   │   ├── dto/
│   │   └── middleware/
│   │
│   ├── compiler/
│   │   ├── c/
│   │   ├── cpp/
│   │   ├── java/
│   │   └── python/
│   │
│   ├── sandbox/
│   │   ├── nsjail/
│   │   ├── cgroup/
│   │   └── security/
│   │
│   ├── workspace/
│   │
│   ├── queue/
│   │   └── redis/
│   │
│   ├── storage/
│   │
│   ├── comparison/
│   │
│   ├── scheduler/
│   │
│   ├── worker/
│   │
│   ├── config/
│   │
│   ├── events/
│   │
│   ├── observability/
│   │   ├── logging/
│   │   ├── metrics/
│   │   └── tracing/
│   │
│   └── health/
│
├── configs/
│   └── judge.conf
│
├── docker/
│   ├── server/
│   │   └── Dockerfile
│   └── worker/
│       └── Dockerfile
│
├── deployments/
│   └── docker-compose.yml
│
├── tests/
│   ├── integration/
│   ├── security/
│   ├── performance/
│   └── load/
│
├── benchmarks/
│
├── go.mod
└── README.md
```

The exact package names can be refined during implementation, but the separation must remain.

---

# 10. Domain Layer

The domain layer contains concepts that have meaning independent of infrastructure.

Examples:

```text
Submission
TestCase
Language
ResourceLimits
ExecutionResult
CompilationResult
Artifact
ComparisonResult
SubmissionStatus
TestcaseStatus
```

The domain layer must not import:

```text
Redis
Docker SDK
NSJail
net/http
Prometheus
filesystem infrastructure
```

This keeps the core model portable and highly testable.

---

# 11. Application Layer

The application layer coordinates use cases.

Examples:

```text
CreateSubmission
ExecuteSubmission
CompileSubmission
RunTestCase
AggregateResults
CancelSubmission
RecoverJobs
```

It coordinates domain objects and injected infrastructure interfaces.

It should contain the actual orchestration logic.

---

# 12. Infrastructure Layer

Infrastructure implements external concerns:

```text
Redis queue
filesystem storage
Docker management
NSJail execution
cgroups
HTTP server
logging
metrics
tracing
```

Infrastructure must not leak its implementation details into the domain.

---

# 13. API Layer

HTTP handlers should be thin.

They should:

1. parse request;
2. validate transport-level requirements;
3. map DTO → domain/application request;
4. invoke application service;
5. map result → DTO;
6. write response.

A handler should not compile code or spawn processes.

Bad:

```text
HTTP Handler
 ├── Redis
 ├── Docker
 ├── GCC
 ├── NSJail
 ├── comparison
 └── filesystem
```

Good:

```text
HTTP Handler
      │
      ▼
SubmissionService
      │
      ▼
Scheduler
```

---

# 14. Execution Pipeline

The finalized execution pipeline is:

```text
HTTP Request
    │
    ▼
API Validation
    │
    ▼
Submission Domain Object
    │
    ▼
Scheduler
    │
    ▼
Queue
    │
    ▼
Worker
    │
    ▼
Language Registry
    │
    ▼
Compiler
    │
    ▼
Compiled Artifact
    │
    ▼
Testcase Scheduler
    │
    ├─────────────┬─────────────┐
    ▼             ▼             ▼
 Testcase 1    Testcase 2    Testcase N
    │             │             │
    ▼             ▼             ▼
 NSJail         NSJail         NSJail
    │             │             │
    ▼             ▼             ▼
 Result         Result         Result
    └─────────────┴─────────────┘
                  │
                  ▼
          Comparison Engine
                  │
                  ▼
          Result Aggregator
                  │
                  ▼
             HTTP Response
```

---

# 15. Docker Strategy

Do **not** create a Docker container for every submission or testcase.

Use long-lived worker containers.

Recommended initial deployment:

```text
judge-server container
judge-worker container
redis container
```

The worker contains:

```text
Go worker
GCC
G++
OpenJDK
CPython
NSJail
required runtime dependencies
```

Docker provides the outer infrastructure boundary.

NSJail/cgroups provide the per-execution boundary.

---

# 16. Sandbox Model

The security model is:

```text
Host
  │
  ▼
Docker worker
  │
  ▼
Go worker
  │
  ▼
NSJail
  │
  ├── namespace isolation
  ├── cgroups
  ├── seccomp
  ├── filesystem restrictions
  ├── PID restrictions
  ├── resource limits
  └── no network
       │
       ▼
  User program
```

The submitted program is never executed directly by the Go process.

---

# 17. Sandbox Security Requirements

Submitted programs must not be able to:

* access the host filesystem;
* access Docker;
* access Redis;
* access the API server;
* access AWS metadata;
* access other submissions;
* access another testcase's writable state;
* create arbitrary mounts;
* create unrestricted devices;
* escape namespaces;
* create unlimited processes;
* exhaust host memory;
* consume unrestricted CPU;
* create unbounded files;
* access the Internet.

Docker capabilities must be minimized.

The worker must not run privileged.

No Docker socket is exposed.

---

# 18. Network

Network access is disabled by default and, for the initial system, entirely disabled.

The sandbox should not have access to:

```text
Internet
DNS
localhost
Redis
API
AWS metadata
```

This is both a security and reproducibility requirement.

---

# 19. Testcase Isolation

Every testcase receives a fresh writable execution environment.

Compiled artifacts may be reused.

Writable state may not.

Therefore:

```text
              compiled artifact
                     │
          ┌──────────┼──────────┐
          ▼          ▼          ▼
       workspace   workspace   workspace
          A           B           C
```

Testcase A must not be able to create state visible to testcase B.

---

# 20. Compilation Strategy

C/C++/Java:

```text
source
  │
  ▼
compile once
  │
  ▼
immutable artifact
  │
  ├── testcase 1
  ├── testcase 2
  ├── testcase 3
  └── testcase N
```

Python:

```text
source
  │
  ▼
prepared source
  │
  ├── testcase 1
  ├── testcase 2
  ├── testcase 3
  └── testcase N
```

All testcase execution occurs against the same logical submission artifact.

---

# 21. Testcase Concurrency

All testcases should be eligible for concurrent execution.

However, concurrency is bounded.

The scheduler considers:

```text
CPU capacity
memory capacity
active submissions
submission concurrency limit
worker capacity
```

A submission containing 1,000 testcases must not launch 1,000 processes.

---

# 22. Fairness

The scheduler should prevent one submission from monopolizing the entire machine.

For example:

```text
User A = 1000 testcases
User B = 10 testcases
User C = 10 testcases
```

The scheduler should be capable of interleaving execution.

A reasonable initial strategy is bounded per-submission concurrency combined with a fair queue.

---

# 23. Resource Limits

Default limits should be configurable through `judge.conf`.

Baseline execution configuration:

```toml
[limits.execution]
cpu_time_seconds = 2
cpu_extra_time_seconds = 0.5
wall_time_seconds = 4
memory_mb = 256
stack_mb = 64
max_processes = 32
max_file_mb = 16
stdout_mb = 1
stderr_mb = 1
```

Compilation gets separate limits.

---

# 24. Submission Limits

```toml
[limits.submission]
max_testcases = 1000
max_source_mb = 1
max_total_input_mb = 32
max_total_output_mb = 32
max_parallel_testcases = 4
max_total_wall_time_seconds = 60
```

These values are configurable and should be benchmarked.

---

# 25. Resource Admission

The scheduler must reserve resources before starting an execution.

For example:

```text
Host:
8 GB RAM

Execution A:
256 MB

Execution B:
256 MB

Execution C:
256 MB

Execution D:
256 MB
```

Only start executions for which sufficient capacity exists.

This prevents the judge from depending solely on Linux's OOM killer.

---

# 26. Output Safety

stdout and stderr must always be bounded.

Never collect unlimited user output into memory.

Use bounded streaming writers.

When output exceeds the limit:

```text
output_limit_exceeded
```

The execution's complete process tree must be terminated.

---

# 27. Process Safety

Fork bombs and process bombs must be contained using:

* PID namespace;
* cgroups v2;
* process limits;
* rlimits where appropriate;
* process-group/cgroup termination.

Killing the parent must not leave descendants alive.

---

# 28. Result Semantics

Every testcase returns:

```text
index
status
stdout
stderr
exit code
exit signal
CPU time
wall time
memory usage
```

Compilation returns:

```text
status
stdout/stderr where applicable
time
memory where available
```

The submission returns aggregate status and all testcase results.

---

# 29. Testcase Failure Behavior

All testcases execute.

Example:

```text
TC1 → Accepted
TC2 → Wrong Answer
TC3 → Runtime Error
TC4 → Accepted
TC5 → Time Limit Exceeded
```

The final result contains all five.

Execution ordering is not guaranteed.

Testcase index is the authoritative identity.

---

# 30. Comparison

Comparison is a standalone module.

Execution and comparison must not be coupled.

```text
ExecutionResult
      │
      ▼
Comparator
      │
      ▼
Accepted / Wrong Answer
```

The initial comparison behavior should follow Judge0's normal expected-output semantics.

The architecture must allow custom comparison policies to be added later.

---

# 31. Redis

Redis Streams should be used for transient reliable coordination where appropriate.

Potential streams:

```text
judge:submissions
judge:executions
judge:events
```

Consumer groups provide:

* job acknowledgement;
* pending-job recovery;
* worker coordination;
* controlled retries.

Redis should not contain large binaries or testcase payloads unnecessarily.

---

# 32. No Permanent Storage

The system is intentionally ephemeral.

After a successful client response:

```text
submission metadata → delete
testcase input → delete
execution workspace → delete
artifact → delete or expire
events → expire
```

Compilation caches may optionally live longer.

---

# 33. Failure Handling

The system must distinguish:

```text
User program failure
Sandbox failure
Worker failure
Queue failure
Application failure
Configuration failure
```

A user program failure is normal judge behavior.

It must never cause an application restart.

---

# 34. Retry Policy

Jobs must have a bounded retry count.

Example:

```toml
[jobs]
max_attempts = 2
```

A repeated infrastructure failure becomes:

```text
system_error
```

rather than:

```text
retry forever
```

This explicitly prevents crash/restart/requeue loops.

---

# 35. Worker Lifecycle

Workers are long-lived.

Lifecycle:

```text
starting
   │
   ▼
ready
   │
   ▼
busy
   │
   ▼
draining
   │
   ▼
stopped
```

Workers can be recycled based on:

```toml
[worker.recycle]
max_jobs = 10000
max_uptime_minutes = 360
```

Worker recycling must be graceful.

---

# 36. Server Reliability

The API server should remain alive if:

* Redis temporarily disappears;
* a worker dies;
* a submission fails;
* a compiler crashes;
* NSJail fails;
* a testcase hangs;
* a testcase is killed;
* output limits are exceeded.

Fatal startup configuration errors may terminate the process.

Runtime submission errors must generally be represented as structured failures.

---

# 37. Graceful Shutdown

On SIGTERM:

```text
stop accepting submissions
        │
        ▼
stop scheduling new jobs
        │
        ▼
finish/requeue recoverable jobs
        │
        ▼
terminate active sandboxes
        │
        ▼
shutdown workers
        │
        ▼
shutdown HTTP
```

All process trees must be cleaned.

---

# 38. Configuration

Configuration must be externalized into:

```text
judge.conf
```

TOML is preferred.

Configuration should contain:

```text
server
redis
workers
scheduler
limits
languages
sandbox
worker recycling
observability
```

No important production behavior should be hardcoded.

---

# 39. Observability

The system should use structured logging and metrics from the beginning.

Metrics:

```text
requests
submissions
queue depth
queue latency
compile latency
execution latency
testcases executed
active testcases
worker utilization
worker failures
sandbox failures
CPU usage
memory usage
output sizes
retries
```

Tracing can use OpenTelemetry.

---

# 40. Health

Endpoints:

```text
GET /health
GET /ready
GET /metrics
```

`/health` answers whether the process is alive.

`/ready` answers whether the system can currently accept work.

A Redis outage should not necessarily kill the server.

Instead, readiness can become false/degraded until the dependency recovers.

---

# 41. Testing Strategy

The implementation must include:

### Unit tests

* domain objects;
* validators;
* scheduler;
* comparison;
* resource admission;
* status mapping;
* configuration.

### Integration tests

* Redis;
* compiler;
* worker;
* NSJail;
* cgroups;
* Docker;
* complete API → worker pipeline.

### Security tests

Explicitly test:

```text
fork bombs
memory bombs
CPU loops
output bombs
file bombs
network access
filesystem traversal
symlink attacks
/proc access
mount attempts
capability abuse
child process persistence
```

### Load tests

Test:

```text
1 concurrent submission
10
50
100
200+
```

with varying testcase counts.

---

# 42. Performance Benchmarking

Performance must be measured on the target hardware.

Benchmark:

```text
compile latency
single testcase latency
10 testcases
100 testcases
parallel testcase execution
100 concurrent users
queue latency
worker utilization
sandbox startup
worker recycle
Redis overhead
```

Record:

```text
p50
p95
p99
throughput
CPU
RAM
queue wait
```

The system should be optimized based on benchmark results rather than assumptions.

---

# 43. Initial 2 CPU / 8 GB Configuration

Initial configuration should be conservative:

```toml
[workers]
min = 1
max = 1
execution_slots = 2

[scheduler]
max_concurrent_submissions = 2
```

The scheduler should prevent the system from exceeding available memory.

When deployed on a larger EC2 instance, concurrency can be increased through configuration.

---

# 44. Vertical Scaling

The same architecture should support:

```text
2 CPU / 8 GB
8 CPU / 16 GB
16 CPU / 32 GB
32 CPU / 64 GB
```

without code changes.

Only capacity configuration should need adjustment.

The system does not initially require horizontal scaling, Kubernetes, or multiple machines.

---

# 45. API Model

The external API should remain simple:

```text
POST /submissions
GET  /submissions/{id}
GET  /submissions/{id}/stream
GET  /languages
GET  /health
GET  /ready
GET  /metrics
```

The main request contains:

```text
language
source code
testcases
resource limits
optional compiler/runtime arguments
```

The internal domain model must remain independent from these HTTP structures.

---

# 46. Base64

Base64 should be supported at the API boundary.

Decode immediately.

Internally:

```text
HTTP Base64
      │
      ▼
[]byte
      │
      ▼
application/domain
```

Do not repeatedly encode/decode data internally.

---

# 47. Streaming

SSE may expose:

```text
queued
compiling
compiled
testcase started
testcase finished
completed
```

However, execution must never depend on a client maintaining the streaming connection.

The client can disconnect without corrupting execution state.

---

# 48. Language Registry

Languages should be registered through a typed registry.

Conceptually:

```go
type LanguageDefinition struct {
    ID             LanguageID
    Name           string
    SourceFilename string
    Compiler       Compiler
    Runtime        Runtime
}
```

Initial implementations:

```text
C
C++
Java
Python
```

Adding a language should require implementing its compiler/runtime behavior rather than modifying the scheduler.

---

# 49. No Runtime Package Installation

The execution environment is immutable.

Python:

```text
standard library only
```

C/C++:

```text
installed compiler/toolchain
```

Java:

```text
installed JDK
```

No submission can install dependencies.

No submission can download packages.

---

# 50. Environment Sanitization

User programs receive a deliberately constructed environment.

Do not inherit the worker's entire environment.

Only explicitly approved environment variables should be passed.

This prevents accidental exposure of:

```text
Redis credentials
AWS credentials
Docker configuration
application configuration
internal paths
secrets
```

---

# 51. Security Boundary Principle

The implementation must follow:

> **Infrastructure may be reused; untrusted mutable execution state must not be reused.**

Therefore:

```text
Reuse:
Docker container
compiler installation
language runtime
immutable artifacts
safe caches

Do not reuse:
writable testcase filesystem
process state
environment state
user-created files
process trees
mutable sandbox state
```

---

# 52. Operational Principle

The system must prefer **degradation over catastrophic failure**.

For example:

```text
No worker available
    ↓
queue / controlled overload
```

not:

```text
No worker available
    ↓
panic
    ↓
restart
    ↓
retry
    ↓
panic
```

Likewise:

```text
Redis unavailable
```

should cause a controlled degraded state rather than an uncontrolled restart loop.

---

# 53. Final Architecture

```text
                                      CLIENTS
                                         │
                                         │ HTTP
                                         ▼
                         ┌──────────────────────────┐
                         │       GO SERVER          │
                         │                          │
                         │ API                      │
                         │ Validation               │
                         │ Submission Service       │
                         │ Scheduler                │
                         │ Result Aggregator        │
                         │ Event Publisher          │
                         │ Health / Metrics         │
                         └────────────┬─────────────┘
                                      │
                                      ▼
                              ┌───────────────┐
                              │     Redis     │
                              │ Redis Streams │
                              └───────┬───────┘
                                      │
                                      ▼
                         ┌──────────────────────────┐
                         │       GO WORKER          │
                         │                          │
                         │ Language Registry        │
                         │ Compiler Manager         │
                         │ Artifact Manager          │
                         │ Execution Manager        │
                         │ Testcase Scheduler       │
                         │ Workspace Manager       │
                         │ Resource Manager         │
                         └────────────┬─────────────┘
                                      │
                              Docker boundary
                                      │
                                      ▼
                         ┌──────────────────────────┐
                         │        NSJAIL            │
                         │                          │
                         │ PID namespace            │
                         │ Mount namespace          │
                         │ Network namespace        │
                         │ cgroups v2               │
                         │ seccomp                  │
                         │ rlimits                  │
                         │ filesystem isolation     │
                         └────────────┬─────────────┘
                                      │
                         ┌────────────┼────────────┐
                         ▼            ▼            ▼
                      Testcase     Testcase      Testcase
                         1            2             N
                         │            │             │
                         └────────────┼─────────────┘
                                      │
                                      ▼
                              Comparison Engine
                                      │
                                      ▼
                              Result Aggregator
                                      │
                                      ▼
                                  GO API
                                      │
                                      ▼
                                   CLIENT
```

---

# 54. Final Design Decisions

The finalized system therefore uses:

| Area                   | Decision                                                       |
| ---------------------- | -------------------------------------------------------------- |
| Implementation         | Go                                                             |
| Architecture           | Modular, layered, dependency-injected                          |
| Composition            | Explicit composition root                                      |
| Typing                 | Strong domain types                                            |
| API                    | Judge0-inspired                                                |
| Protocol               | HTTP                                                           |
| Streaming              | SSE optional                                                   |
| Languages              | C, C++, Java, Python                                           |
| Python packages        | Standard library only                                          |
| Network                | Disabled                                                       |
| Deployment             | Single machine                                                 |
| Horizontal scaling     | Not required                                                   |
| Vertical scaling       | Supported                                                      |
| Sandbox                | Docker + NSJail + cgroups                                      |
| Worker lifecycle       | Long-lived                                                     |
| Container per testcase | No                                                             |
| Compilation            | Once per submission                                            |
| Testcase execution     | Concurrent, bounded                                            |
| Testcase failure       | Continue remaining tests                                       |
| Test ordering          | Not significant                                                |
| Queue                  | Redis Streams                                                  |
| Persistent DB          | Not required                                                   |
| Result retention       | Temporary                                                      |
| Resource controls      | CPU, wall time, memory, stack, processes, files, stdout/stderr |
| Output                 | Per-testcase                                                   |
| Memory reporting       | Yes                                                            |
| CPU/time reporting     | Yes                                                            |
| Authentication         | Not initially                                                  |
| Rate limiting          | Not initially                                                  |
| Observability          | Structured logs + metrics + optional tracing                   |
| Reliability            | Bounded retries + worker recovery                              |
| Worker recycling       | Yes                                                            |
| Configuration          | `judge.conf`                                                   |
| Initial baseline       | 2 CPU / 8 GB                                                   |
| Primary optimization   | Throughput + low execution latency                             |

---

# 55. Final Engineering Requirement

The implementation must **not** be developed as one large Go application with handlers, queue logic, compiler logic, sandboxing, and process management mixed together.

It must be developed as a set of cohesive modules with explicit boundaries:

```text
API
  ↓
Application
  ↓
Domain
  ↓
Infrastructure adapters
```

with dependencies assembled in the composition root.

All concurrency, process management, resource management, sandboxing, filesystem operations, queue interaction, and external integrations must have clear ownership.

The design should favor:

* small cohesive packages;
* explicit dependencies;
* strong types;
* immutable configuration after startup;
* context-aware operations;
* deterministic cleanup;
* bounded concurrency;
* bounded memory;
* explicit error propagation;
* structured errors;
* graceful lifecycle management;
* dependency inversion;
* table-driven tests;
* race-detector-clean code;
* benchmark-driven optimization;
* static analysis;
* reproducible builds.

The result should be a **production-quality Go code judge**, not merely a working code-execution script.

The fundamental architectural invariant is:

```text
                    ┌───────────────────────┐
                    │      GO CONTROL       │
                    │        PLANE          │
                    └───────────┬───────────┘
                                │
                       trusted boundary
                                │
                    ┌───────────▼───────────┐
                    │     GO WORKER         │
                    └───────────┬───────────┘
                                │
                       sandbox boundary
                                │
                    ┌───────────▼───────────┐
                    │      NSJAIL           │
                    │      CGROUP           │
                    │      SECCOMP          │
                    └───────────┬───────────┘
                                │
                       untrusted boundary
                                │
                    ┌───────────▼───────────┐
                    │    USER PROGRAM       │
                    └───────────────────────┘
```

**Infrastructure is reusable. User execution state is disposable.**

That principle, combined with Go's explicit composition and type-safe modular architecture, should be the foundation of the implementation.
