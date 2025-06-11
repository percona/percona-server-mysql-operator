#!/usr/bin/env bash

find ../../e2e-tests/tests -type f -name '*.yaml' -exec "$(which gsed || which sed)" -i -E "s/^([[:space:]]*)app\.kubernetes\.io\/version:.*/\1app.kubernetes.io\/version: v$(<version.txt)/" {} +
