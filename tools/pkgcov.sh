#!/usr/bin/env sh
# Prints statement coverage per package from a coverage profile, counting a
# block as covered if any test binary ran it (as -coverpkg=./... produces).
# Usage: tools/pkgcov.sh [coverage.out]
profile=${1:-coverage.out}
grep -v '^mode:' "$profile" | awk '
{
  split($1, loc, ":"); file = loc[1]; n = $2; hit = $3
  key = $1
  if (!(key in stmts)) { stmts[key] = n; pkgof[key] = file; sub("/[^/]*$", "", pkgof[key]) }
  if (hit > 0) covered[key] = 1
}
END {
  for (k in stmts) { p = pkgof[k]; total[p] += stmts[k]; all += stmts[k]; if (k in covered) { cov[p] += stmts[k]; allcov += stmts[k] } }
  for (p in total) printf "%6.1f%%  %s\n", 100 * cov[p] / total[p], p
  printf "%6.1f%%  total\n", 100 * allcov / all
}' | sort -k2
