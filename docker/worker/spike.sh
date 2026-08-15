#!/bin/bash
# Phase 0 sandbox spike: prove nsjail + cgroup v2 work in a non-privileged container.
# Each check prints PASS/FAIL; the exit code is the number of failures.
set -u

fails=0
check() { # check <name> <expected: PASS|FAIL-is-ok> ; reads result from $?
    if [ "$1" = 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fails=$((fails + 1)); fi
}

echo "== environment =="
echo "kernel:     $(uname -r)"
echo "cgroup fs:  $(stat -fc %T /sys/fs/cgroup)"
echo "capsh:      $(grep ^CapEff /proc/self/status)"
echo "nsjail:     $(nsjail --help 2>&1 | head -1)"
echo

echo "== 1. namespace creation =="
nsjail -Mo --quiet --user 65534 --group 65534 \
    --iface_no_lo --disable_clone_newcgroup \
    --rlimit_as max --time_limit 10 \
    -- /bin/echo ns-ok 2>/tmp/ns.err | grep -q ns-ok
check $? "nsjail creates user/pid/mount/net namespaces"
[ -s /tmp/ns.err ] && sed 's/^/      | /' /tmp/ns.err

echo
echo "== 2. cgroup v2 delegation =="
if [ -w /sys/fs/cgroup ]; then
    echo "PASS  /sys/fs/cgroup already writable"
else
    mount -t cgroup2 none /sys/fs/cgroup 2>/tmp/cg.err
    check $? "remounted cgroup2 rw"
    [ -s /tmp/cg.err ] && sed 's/^/      | /' /tmp/cg.err
fi
echo "+memory +pids" >/sys/fs/cgroup/cgroup.subtree_control 2>/tmp/sub.err
check $? "enabled memory+pids controllers in subtree_control"
[ -s /tmp/sub.err ] && sed 's/^/      | /' /tmp/sub.err
echo "      controllers: $(cat /sys/fs/cgroup/cgroup.controllers 2>/dev/null)"

echo
echo "== 3. memory bomb contained =="
cat >/tmp/membomb.c <<'EOF'
#include <stdlib.h>
#include <string.h>
int main(void) {
    for (;;) { char *p = malloc(8 << 20); if (!p) return 7; memset(p, 1, 8 << 20); }
}
EOF
gcc -O0 -o /tmp/membomb /tmp/membomb.c
nsjail -Mo --quiet --user 65534 --group 65534 --iface_no_lo --disable_clone_newcgroup \
    --use_cgroupv2 --cgroupv2_mount=/sys/fs/cgroup \
    --cgroup_mem_max=$((256 * 1024 * 1024)) --cgroup_pids_max=32 \
    --rlimit_as max --time_limit 10 \
    --bindmount_ro /tmp/membomb:/membomb \
    -- /membomb >/dev/null 2>/tmp/mem.err
rc=$?
[ $rc -ne 0 ]
check $? "memory bomb killed (exit=$rc, not OOM-killing the container)"
sed 's/^/      | /' /tmp/mem.err | tail -3

echo
echo "== 4. fork bomb contained =="
timeout 20 nsjail -Mo --quiet --user 65534 --group 65534 --iface_no_lo --disable_clone_newcgroup \
    --use_cgroupv2 --cgroupv2_mount=/sys/fs/cgroup \
    --cgroup_pids_max=16 --cgroup_mem_max=$((128 * 1024 * 1024)) \
    --rlimit_as max --time_limit 5 \
    -- /bin/bash -c ':(){ :|:& };:' >/dev/null 2>/tmp/fork.err
rc=$?
[ $rc -ne 0 ]
check $? "fork bomb terminated (exit=$rc)"
sleep 1
survivors=$(pgrep -c bash 2>/dev/null || echo 0)
[ "$survivors" -le 1 ]
check $? "no surviving descendants (bash procs: $survivors)"

echo
echo "== 5. network denied =="
nsjail -Mo --quiet --user 65534 --group 65534 --iface_no_lo --disable_clone_newcgroup \
    --rlimit_as max --time_limit 10 \
    -- /bin/bash -c 'exec 3<>/dev/tcp/1.1.1.1/53' >/dev/null 2>&1
[ $? -ne 0 ]
check $? "outbound TCP blocked inside jail"

echo
echo "== 6. memory accounting readback =="
peak_supported=no
probe=/sys/fs/cgroup/judge-probe
mkdir -p $probe 2>/dev/null && [ -f $probe/memory.peak ] && peak_supported=yes
rmdir $probe 2>/dev/null
[ "$peak_supported" = yes ]
check $? "memory.peak available for per-execution accounting"

echo
echo "failures: $fails"
exit $fails
