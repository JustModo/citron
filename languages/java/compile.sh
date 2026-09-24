#!/bin/sh
# Usage: compile.sh SOURCE
#
# javac requires the file to be named after its public top-level type, and the class
# to run is whichever declares main(), which need not be that type. This names the
# file, compiles it, and writes the main class to main.args for `java @main.args`.
set -eu

src=$1
jvm="-J-XX:+UseSerialGC -J-XX:TieredStopAtLevel=1"

name=$(sed -nE 's/^[[:space:]]*public[[:space:]]+((final|abstract|strictfp|sealed|non-sealed)[[:space:]]+)*(class|interface|enum|record)[[:space:]]+([A-Za-z_][A-Za-z0-9_]{0,63})([^A-Za-z0-9_$].*)?$/\4/p' "$src" | head -n 1)
if [ -n "$name" ] && [ "$name.java" != "$src" ]; then
    mv -- "$src" "$name.java"
    src=$name.java
fi

javac $jvm -nowarn -d . "$src"

set -- *.class
if [ ! -e "$1" ]; then
    echo "error: no classes in the default package" >&2
    exit 1
fi
classes=$(printf '%s\n' "$@" | grep -v '\$' | sed 's/\.class$//')

# javap prints a "... class Name ... {" header before each class's members.
main=$(javap $jvm -cp . $classes | awk '
    /^[^ ].*[{]$/ {
        for (i = 1; i < NF; i++)
            if ($i ~ /^(class|interface|enum|record)$/) { cls = $(i + 1); sub(/<.*/, "", cls); break }
    }
    /public static void main\(java\.lang\.String(\[\]|\.\.\.)\)/ { print cls; exit }')

if [ -z "$main" ]; then
    echo "error: no class declares public static void main(String[])" >&2
    exit 1
fi
printf '%s\n' "$main" > main.args
