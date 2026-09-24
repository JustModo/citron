#!/bin/sh
set -e

CG=/sys/fs/cgroup
V1_CONTROLLERS="memory pids cpuacct cpu,cpuacct cpu cpuset"
V1_REQUIRED="memory pids cpu cpuacct"

make_writable() {
    mount -o remount,rw "$1" 2>/dev/null ||
    mount -t cgroup none "$1" -o "rw,$2" 2>/dev/null
}

setup_v1() {
    for c in $V1_CONTROLLERS; do
        [ -d "$CG/$c" ] || continue
        make_writable "$CG/$c" "$c" || true
        mkdir -p "$CG/$c/citron" 2>/dev/null || true
        chown -R citron:citron "$CG/$c/citron" 2>/dev/null || true
    done

    for c in $V1_REQUIRED; do
        [ -w "$CG/$c/citron" ] || return 1
    done
}

setup_v2() {
    [ -w "$CG/cgroup.procs" ] || mount -t cgroup2 none "$CG" 2>/dev/null || return 1
    [ -w "$CG/cgroup.procs" ] || return 1

    mkdir -p "$CG/init"
    while read -r pid; do
        echo "$pid" > "$CG/init/cgroup.procs" 2>/dev/null || true
    done < "$CG/cgroup.procs"

    echo "+memory +pids +cpu" > "$CG/cgroup.subtree_control" 2>/dev/null || true
    mkdir -p "$CG/citron"
    echo "+memory +pids +cpu" > "$CG/citron/cgroup.subtree_control" 2>/dev/null || true
    chown -R citron:citron "$CG/citron"
}

has_v1=false
for c in $V1_CONTROLLERS; do
    [ -d "$CG/$c" ] && has_v1=true && break
done

if $has_v1; then
    setup_v1
else
    setup_v2
fi || {
    echo "entrypoint: no usable cgroup hierarchy found. Refusing to start." >&2
    exit 1
}

exec "$@"
