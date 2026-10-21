#!/usr/bin/env sh
# Fails if any non-test file in internal/cli defines more or less than one
# cobra command (files with no command, like helpers, are fine). This repo
# keeps its commands in internal/cli rather than cmd/.
dir=${1:-internal/cli}
fail=0
for f in "$dir"/*.go; do
  case "$f" in *_test.go) continue ;; esac
  n=$(grep -c '&cobra.Command{' "$f")
  if [ "$n" -gt 1 ]; then
    echo "$f defines $n commands"
    fail=1
  fi
done
exit $fail
