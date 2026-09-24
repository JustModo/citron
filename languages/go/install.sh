#!/bin/sh
# Compiles the standard library once, so each submission compiles and links in
# well under a second without a writable build cache.
set -eu
export CGO_ENABLED=0 GOCACHE="$PWD/cache"
go list -export -f '{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}' std > importcfg
go env GOTOOLDIR > tooldir
