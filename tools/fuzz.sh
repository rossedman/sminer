#!/usr/bin/env sh
# Runs every Fuzz* target in the module, one at a time, for FUZZTIME each.
# Usage: FUZZTIME=30s tools/fuzz.sh
set -eu
fuzztime=${FUZZTIME:-30s}
grep -rl --include='*_test.go' '^func Fuzz' . | sort | while read -r file; do
  pkg=./$(dirname "${file#./}")
  sed -n 's/^func \(Fuzz[A-Za-z0-9_]*\)(.*/\1/p' "$file" | while read -r name; do
    echo "== $pkg $name ($fuzztime)"
    go test "$pkg" -run '^$' -fuzz "^${name}\$" -fuzztime "$fuzztime"
  done
done
