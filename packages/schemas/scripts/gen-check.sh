#!/bin/sh
# Fails when timeline/v1.ts or timeline/ops.v1.ts is not what `bun run gen` produces from the schemas (CI drift check).
set -eu
cd "$(dirname "$0")/.."
bun run gen
# `git diff` catches a changed file; `git status` also catches one that was never committed.
if ! git diff --exit-code -- timeline/v1.ts timeline/ops.v1.ts || [ -n "$(git status --porcelain -- timeline/v1.ts timeline/ops.v1.ts)" ]; then
  echo "timeline/v1.ts or timeline/ops.v1.ts is out of date: run 'bun run gen' in packages/schemas and commit the result" >&2
  exit 1
fi
