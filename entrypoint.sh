#!/bin/sh
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
echo "+memory +pids +cpu" > $CG/citron/cgroup.subtree_control
chown -R citron:citron $CG/citron

exec "$@"
