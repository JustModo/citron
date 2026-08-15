# Sandbox spike — findings

Phase 0. Question: does nsjail + cgroup v2 work inside a **non-privileged** Docker
container? Answer: yes, with three specific flags that are not obvious. Re-run with
`make spike`.

Verified on kernel 7.1.8 / Docker 29.7.2 / nsjail 3.6 / cgroup2.

## The working configuration

Container:

```
--cap-add=SYS_ADMIN                    # namespace creation; NOT --privileged
--security-opt apparmor=unconfined     # docker-default denies mount()
--security-opt systempaths=unconfined  # see "procfs" below
--cgroupns=private
```

Entrypoint must run the cgroup delegation dance ([entrypoint.sh](../docker/worker/entrypoint.sh))
before anything else.

nsjail, per execution:

```
--mode o --quiet --user 65534 --group 65534
--iface_no_lo                          # no network of any kind
--disable_clone_newcgroup --no_pivotroot --chroot /
--use_cgroupv2 --cgroupv2_mount=/sys/fs/cgroup/judge
--cgroup_mem_max=<bytes> --cgroup_pids_max=<n>
--time_limit=<wall>
--rlimit_as max                        # deliberate; see below
```

## The three non-obvious blockers

**1. procfs.** Docker masks `/proc/kcore`, `/proc/keys` and friends by bind-mounting
`/dev/null` over them. The kernel refuses to mount a fresh procfs when the source
`/proc` has overmounts you don't fully own, so nsjail died on
`Failed to mount mandatory point: '/proc'`. Fixed by `--security-opt systempaths=unconfined`.

Trade-off, stated plainly: this un-masks those paths **for the worker container**, which
is trusted code. The **jailed** process still gets a fresh procfs in its own PID
namespace and cannot see them. The security suite asserts this.

**2. cgroup controllers.** `echo +memory > cgroup.subtree_control` fails with `EBUSY`
because the kernel forbids enabling controllers on a cgroup that holds processes (the
"no internal processes" rule), and the container's own root cgroup holds PID 1. The
entrypoint moves every pid into `/sys/fs/cgroup/init` first, then enables
`+memory +pids +cpu`, then creates an empty `/sys/fs/cgroup/judge` for nsjail to
create per-execution children under.

nsjail's own error message suggests `--cgroupns=host`. Don't: that hands the container
the host's full cgroup hierarchy. The dance above keeps `--cgroupns=private`.

**3. pivot_root.** `pivot_root` returns `EPERM` in a container. nsjail ships
`--no_pivotroot`, which falls back to `MS_MOVE` + `chroot`. Weaker in isolation, but the
jailed process holds no capabilities, has a private mount namespace, a fresh PID
namespace and seccomp — a chroot escape needs a capability it does not have.

## Results

| Check | Result |
|---|---|
| Namespace creation (user/pid/mount/net) | pass |
| Delegated writable cgroup with memory+pids | pass |
| Jail launches with cgroup limits applied | pass |
| Memory bomb killed at limit | pass — SIGKILL, host RSS moved 27 MB against a 256 MB cap |
| Fork bomb leaves no survivors | pass — process count unchanged |
| Outbound TCP blocked | pass |
| `memory.peak` readable for accounting | pass |

## Consequences for the implementation

- **Memory accounting comes from the cgroup** (`memory.peak`), not `ru_maxrss`. The
  local dev driver has to use `ru_maxrss` and will therefore report different numbers —
  label the source in results so nobody chases a phantom bug.
- **Never set `RLIMIT_AS`.** The JVM reserves ~1 GB of virtual address space regardless
  of heap size, so an address-space cap kills it at startup. Memory is capped by
  `memory.max` on the cgroup, which counts touched pages. `--rlimit_as max` is
  deliberate, not an oversight.
- `--chroot /` gives the jail a read-only view of the image. The per-testcase writable
  workspace is bind-mounted separately, which is what §19 and §51 require.
- If a future host cannot provide the delegated cgroup, the entrypoint **refuses to
  start** rather than running with unenforced memory limits.
