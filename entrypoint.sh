#!/bin/sh
# cgroup v2 delegation for the worker container.
#
# Two kernel rules make this necessary:
#   1. /sys/fs/cgroup is mounted read-only by Docker, so it must be remounted rw.
#   2. Controllers cannot be enabled on a cgroup that holds processes ("no internal
#      processes"), so every pid must first be moved into a leaf cgroup.
#
# The result is an empty, controller-enabled /sys/fs/cgroup/citron that nsjail can
# create one child cgroup per execution under.
set -e

CG=/sys/fs/cgroup

if ! [ -w $CG/cgroup.procs ]; then
    mount -t cgroup2 none $CG 2>/dev/null || {
        echo "entrypoint: cannot get a writable cgroup2 mount; sandbox memory and" >&2
        echo "entrypoint: pid limits will NOT be enforced. Refusing to start." >&2
        exit 1
    }
fi

mkdir -p $CG/init
while read -r pid; do
    echo "$pid" > $CG/init/cgroup.procs 2>/dev/null || true
done < $CG/cgroup.procs

echo "+memory +pids +cpu" > $CG/cgroup.subtree_control
mkdir -p $CG/citron
# Controllers must be enabled at every level: a per-execution cgroup only gets
# memory.max if its parent delegates the memory controller downwards too.
echo "+memory +pids +cpu" > $CG/citron/cgroup.subtree_control
chown -R citron:citron $CG/citron

exec "$@"
