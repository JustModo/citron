#!/bin/sh
# Usage: compile.sh SOURCE
#
# Links against the standard library prebuilt by install.sh. Without it (a pack used
# straight from the repository) this falls back to go build, which compiles the
# standard library into a fresh cache on every run.
set -eu

pack=$(dirname "$0")
if [ ! -f "$pack/importcfg" ]; then
    exec env CGO_ENABLED=0 go build -o main "$1"
fi

tools=$(cat "$pack/tooldir")
"$tools/compile" -p main -complete -importcfg "$pack/importcfg" -o main.a "$1"
"$tools/link" -importcfg "$pack/importcfg" -o main main.a
rm -f main.a
