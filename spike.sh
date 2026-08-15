#!/bin/bash
# Phase 0 sandbox spike: prove nsjail + cgroup v2 work in a non-privileged container.
# Exit code is the number of failed checks.
#
# Every bomb check first runs a control command through the SAME jail flags, so a
# jail that fails to launch is reported as a broken jail, never as "bomb contained".
set -u

fails=0
CG=/sys/fs/cgroup/citron

pass() { echo "PASS  $1"; }
fail() { echo "FAIL  $1"; fails=$((fails + 1)); }
check() { if [ "$1" -eq 0 ]; then pass "$2"; else fail "$2"; fi; }

# --no_pivotroot: pivot_root is EPERM inside a container; nsjail falls back to
# MS_MOVE + chroot. Weaker on paper, but the process still has no capabilities,
# a private mount namespace and seccomp on top.
# --no_pivotroot: pivot_root is EPERM inside a container; nsjail falls back to
#   MS_MOVE + chroot. The process still has no capabilities, a private mount
#   namespace and seccomp on top.
# --chroot /: the image root, mounted READ-ONLY (nsjail's default for chroot).
#   The writable workspace is bind-mounted in separately per execution.
JAIL_BASE="--mode o --quiet --user 65534 --group 65534 --iface_no_lo
           --disable_clone_newcgroup --no_pivotroot --chroot / --rlimit_as max"
JAIL_CG="--use_cgroupv2 --cgroupv2_mount=$CG"

echo "== environment =="
echo "kernel:    $(uname -r)"
echo "cgroup fs: $(stat -fc %T /sys/fs/cgroup)"
echo "nsjail:    $(nsjail --help 2>&1 | grep -oP 'nsjail version \S+' | head -1)"
echo

echo "== 1. namespace creation =="
out=$(nsjail $JAIL_BASE --time_limit 10 -- /bin/echo ns-ok 2>/tmp/ns.err)
if [ "$out" = "ns-ok" ]; then
    pass "nsjail creates user/pid/mount/net namespaces"
else
    fail "nsjail creates user/pid/mount/net namespaces"
    sed 's/^/      | /' /tmp/ns.err | grep -E '\[E\]|\[F\]' | head -4
fi

echo
echo "== 2. cgroup v2 delegation =="
[ -d "$CG" ] && [ -w "$CG" ]
check $? "delegated cgroup $CG exists and is writable"
grep -q memory /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null
check $? "memory controller delegated to children"
echo "      subtree_control: $(cat /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null)"

echo
echo "== 3. jail works WITH cgroup limits (control) =="
out=$(nsjail $JAIL_BASE $JAIL_CG --cgroup_mem_max=$((128 * 1024 * 1024)) \
        --cgroup_pids_max=32 --time_limit 10 -- /bin/echo cg-ok 2>/tmp/cg.err)
if [ "$out" = "cg-ok" ]; then
    pass "jail launches with cgroup limits applied"
    cg_ok=1
else
    fail "jail launches with cgroup limits applied"
    cg_ok=0
    sed 's/^/      | /' /tmp/cg.err | grep -E '\[E\]|\[F\]' | head -4
fi

echo
echo "== 4. memory bomb contained =="
cat >/tmp/membomb.c <<'EOF'
#include <stdlib.h>
#include <string.h>
int main(void) {
    for (;;) { char *p = malloc(8 << 20); if (!p) return 7; memset(p, 1, 8 << 20); }
}
EOF
gcc -O0 -o /tmp/membomb /tmp/membomb.c
if [ "$cg_ok" = 1 ]; then
    before=$(free -m | awk '/^Mem:/{print $3}')
    nsjail $JAIL_BASE $JAIL_CG --cgroup_mem_max=$((256 * 1024 * 1024)) \
        --cgroup_pids_max=32 --time_limit 15 \
        --bindmount_ro /tmp/membomb:/membomb -- /membomb >/dev/null 2>/tmp/mem.err
    rc=$?
    after=$(free -m | awk '/^Mem:/{print $3}')
    grep -qE 'Received SIGKILL|cgroup.*(oom|kill)|exited with status' /tmp/mem.err && killed=0 || killed=1
    [ $rc -ne 0 ] && [ $((after - before)) -lt 400 ]
    check $? "memory bomb killed at the limit (rc=$rc, host mem delta ${before}->${after} MB)"
else
    fail "memory bomb contained (skipped: cgroup jail broken)"
fi

echo
echo "== 5. fork bomb contained =="
if [ "$cg_ok" = 1 ]; then
    before=$(ps -e --no-headers | wc -l)
    timeout 25 nsjail $JAIL_BASE $JAIL_CG --cgroup_pids_max=16 \
        --cgroup_mem_max=$((128 * 1024 * 1024)) --time_limit 5 \
        -- /bin/bash -c ':(){ :|:& };:' >/dev/null 2>/tmp/fork.err
    rc=$?
    sleep 2
    after=$(ps -e --no-headers | wc -l)
    [ $((after - before)) -le 2 ]
    check $? "fork bomb left no survivors (procs ${before} -> ${after}, rc=$rc)"
else
    fail "fork bomb contained (skipped: cgroup jail broken)"
fi

echo
echo "== 6. network denied =="
nsjail $JAIL_BASE --time_limit 10 \
    -- /bin/bash -c 'exec 3<>/dev/tcp/1.1.1.1/53' >/dev/null 2>&1
[ $? -ne 0 ]
check $? "outbound TCP blocked inside jail"

echo
echo "== 7. memory accounting readback =="
probe=$CG/probe
if mkdir -p $probe 2>/dev/null && [ -f $probe/memory.peak ]; then
    pass "memory.peak available for per-execution accounting"
else
    fail "memory.peak available for per-execution accounting"
fi
rmdir $probe 2>/dev/null

echo
echo "failures: $fails"
exit $fails
