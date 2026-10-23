#!/usr/bin/env sh
# Fails when statement coverage of hand-written code is below the minimum.
# Generated files and examples/ are left out of the count.
# Usage: tools/cover-gate.sh coverage.out [min-percent]
set -eu
profile=${1:-coverage.out}
min=${2:-90}
hand=${profile%.out}.hand.out
generated=$(mktemp)
trap 'rm -f "$generated"' EXIT
go run ./tools/generated ./... > "$generated"
{
  head -1 "$profile"
  grep -v '^mode:' "$profile" | grep -vFf "$generated" | grep -v '/examples/' || true
} > "$hand"
total=$(./tools/pkgcov.sh "$hand" | awk '$2 == "total" {sub("%", "", $1); print $1}')
echo "coverage: ${total}% of hand-written statements (minimum ${min}%)"
awk -v t="$total" -v m="$min" 'BEGIN { exit (t + 0 < m + 0) }' || {
  echo "coverage ${total}% is below ${min}%; see tools/pkgcov.sh $hand" >&2
  exit 1
}
