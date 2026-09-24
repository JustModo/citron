#!/bin/sh
set -eu
npm install --global --prefix /usr/local --no-fund --no-audit \
    typescript@7.0.2 @types/node@20.19.43
